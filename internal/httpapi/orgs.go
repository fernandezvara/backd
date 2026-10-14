package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Organizations (design/organizations.md): /v1/{realm}/_orgs for the signed-in user, and
// /v1/{realm}/_admin/orgs for realm administrators.

const (
	codeNotOrgMember   = "not_org_member"
	codeLastOrgManager = "last_org_manager"
	codeSlugTaken      = "slug_taken"
	codeOrgLimit       = "org_limit_reached"
)

func (a *authAPI) orgRoutes(r chi.Router) {
	r.Route("/v1/{realm}/_orgs", func(r chi.Router) {
		r.Use(noStore, a.resolveRealm, withTimeout(a.opTimeout), a.requireSession)
		json := requireContentType("application/json")
		r.With(json).Post("/", a.createOrg)
		r.Get("/", a.listOrgs)
		r.Route("/{org}", func(r chi.Router) {
			r.Get("/", a.getOrg)
			r.With(json).Patch("/", a.updateOrg)
			r.Get("/members", a.orgMembers)
			r.With(json).Patch("/members/{user}", a.updateOrgMember)
			r.Delete("/members/{user}", a.removeOrgMember)
			r.Post("/leave", a.leaveOrg)
		})
	})
}

// orgError maps organization errors, falling back to the sign-in errors.
func orgError(w http.ResponseWriter, r *http.Request, err error) {
	var bad *auth.OrgInvalidError
	switch {
	case errors.Is(err, auth.ErrOrgsDisabled):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, auth.ErrNotOrgMember):
		writeError(w, r, http.StatusForbidden, codeNotOrgMember, "you are not a member of that organization")
	case errors.Is(err, auth.ErrOrgForbidden), errors.Is(err, auth.ErrOrgCreateNotAllowed):
		writeError(w, r, http.StatusForbidden, codeForbidden, err.Error())
	case errors.Is(err, auth.ErrOrgLimit), errors.Is(err, auth.ErrMembershipLimit):
		writeError(w, r, http.StatusConflict, codeOrgLimit, err.Error())
	case errors.Is(err, auth.ErrLastOrgManager):
		writeError(w, r, http.StatusConflict, codeLastOrgManager, err.Error())
	case errors.Is(err, auth.ErrSlugTaken):
		writeError(w, r, http.StatusConflict, codeSlugTaken, err.Error(), Detail{Path: "slug", Reason: "is used by another organization"})
	case errors.Is(err, auth.ErrOrgNotActive):
		writeError(w, r, http.StatusConflict, "org_not_active", err.Error())
	case errors.Is(err, auth.ErrOrgUserNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "user not found")
	case errors.As(err, &bad):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: bad.Field, Reason: bad.Reason})
	default:
		authError(w, r, err)
	}
}

func orgJSON(o auth.Org) map[string]any {
	claims := o.Claims
	if len(claims) == 0 {
		claims = map[string]any{}
	}
	out := map[string]any{
		"id": o.ID, "name": o.Name, "status": o.Status, "require_mfa": o.RequireMFA,
		"created_at": formatTime(o.CreatedAt), "claims": claims,
	}
	if o.Slug != "" {
		out["slug"] = o.Slug
	} else {
		out["slug"] = nil
	}
	return out
}

func roleOriginsJSON(list []auth.EffectiveRole) []map[string]any {
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = map[string]any{"role": e.Role, "origin": e.Origin}
	}
	return out
}

func membershipJSON(m auth.Membership) map[string]any {
	out := orgJSON(m.Org)
	out["roles"] = m.RoleNames()
	out["effective_roles"] = roleOriginsJSON(m.Roles)
	out["teams"] = nonNil(m.Member.Teams)
	return out
}

func memberJSON(v auth.MemberView, now func() string) map[string]any {
	roles := make([]string, 0, len(v.Member.Roles))
	for _, g := range v.Member.Roles {
		roles = append(roles, g.Role)
	}
	out := map[string]any{
		"user": v.Member.User, "roles": roles, "teams": nonNil(v.Member.Teams),
		"effective_roles": roleOriginsJSON(v.Effective), "overlap": nonNil(v.Overlap),
		"joined_at": formatTime(v.Member.JoinedAt),
	}
	if v.Email != "" {
		out["email"] = v.Email
	}
	if v.Member.InvitedBy != "" {
		out["invited_by"] = v.Member.InvitedBy
	}
	return out
}

// createOrg handles POST /_orgs.
func (a *authAPI) createOrg(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "slug": "string?"})
	if !ok {
		return
	}
	slug, _ := f["slug"].(string)
	m, err := usersOf(r).CreateOrg(r.Context(), principalOf(r), f["name"].(string), slug)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipJSON(m))
}

// listOrgs handles GET /_orgs: the caller's organizations.
func (a *authAPI) listOrgs(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).MyOrgs(r.Context(), principalOf(r).User.ID)
	if err != nil {
		orgError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, m := range list {
		items[i] = membershipJSON(m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// getOrg handles GET /_orgs/{org}.
func (a *authAPI) getOrg(w http.ResponseWriter, r *http.Request) {
	m, err := usersOf(r).MembershipOf(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipJSON(m))
}

// updateOrg handles PATCH /_orgs/{org}: name and slug.
func (a *authAPI) updateOrg(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string?", "slug": "string?"})
	if !ok {
		return
	}
	var name, slug *string
	if v, ok := f["name"].(string); ok {
		name = &v
	}
	if v, ok := f["slug"].(string); ok {
		slug = &v
	}
	m, err := usersOf(r).UpdateOrg(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), name, slug)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipJSON(m))
}

// pageParams reads limit and after from the query.
func pageParams(r *http.Request) (limit int, after string) {
	limit = 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 500)
	}
	return limit, r.URL.Query().Get("after")
}

func memberPage(w http.ResponseWriter, list []auth.MemberView, more bool) {
	items := make([]map[string]any, len(list))
	for i, v := range list {
		items[i] = memberJSON(v, nil)
	}
	out := map[string]any{"items": items, "has_more": more}
	if more && len(list) > 0 {
		out["next"] = list[len(list)-1].Member.User
	}
	writeJSON(w, http.StatusOK, out)
}

// orgMembers handles GET /_orgs/{org}/members.
func (a *authAPI) orgMembers(w http.ResponseWriter, r *http.Request) {
	limit, after := pageParams(r)
	list, more, err := usersOf(r).Members(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), after, limit)
	if err != nil {
		orgError(w, r, err)
		return
	}
	memberPage(w, list, more)
}

// updateOrgMember handles PATCH /_orgs/{org}/members/{user}: the member's direct roles.
func (a *authAPI) updateOrgMember(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"roles": "strings"})
	if !ok {
		return
	}
	v, err := usersOf(r).UpdateMember(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "user"), f["roles"].([]string))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v, nil))
}

// removeOrgMember handles DELETE /_orgs/{org}/members/{user}.
func (a *authAPI) removeOrgMember(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RemoveMember(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "user"))
	if err != nil {
		orgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// leaveOrg handles POST /_orgs/{org}/leave.
func (a *authAPI) leaveOrg(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).LeaveOrg(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org")); err != nil {
		orgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Administrators.

func adminOrgError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "organization not found")
	case errors.Is(err, auth.ErrNotOrgMember):
		// An administrator is told which it is.
		writeError(w, r, http.StatusNotFound, codeNotFound, "that user is not a member of the organization")
	default:
		orgError(w, r, err)
	}
}

// adminListOrgs handles GET /orgs?status=.
func (a *adminAPI) listOrgs(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).AdminOrgs(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, o := range list {
		items[i] = orgJSON(o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// adminCreateOrg handles POST /orgs.
func (a *adminAPI) createOrg(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "slug": "string?", "owner": "string"})
	if !ok {
		return
	}
	slug, _ := f["slug"].(string)
	m, err := usersOf(r).AdminCreateOrg(r.Context(), f["name"].(string), slug, f["owner"].(string))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipJSON(m))
}

// adminGetOrg handles GET /orgs/{org}.
func (a *adminAPI) getOrg(w http.ResponseWriter, r *http.Request) {
	o, err := usersOf(r).AdminOrg(r.Context(), chi.URLParam(r, "org"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, orgJSON(o))
}

// adminOrgMembers handles GET /orgs/{org}/members.
func (a *adminAPI) orgMembers(w http.ResponseWriter, r *http.Request) {
	limit, after := pageParams(r)
	list, more, err := usersOf(r).AdminMembers(r.Context(), chi.URLParam(r, "org"), after, limit)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	memberPage(w, list, more)
}

// addOrgMember handles POST /orgs/{org}/members: a direct add, audited.
func (a *adminAPI) addOrgMember(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"user": "string", "roles": "strings?"})
	if !ok {
		return
	}
	roles, _ := f["roles"].([]string)
	v, err := usersOf(r).AdminAddMember(r.Context(), chi.URLParam(r, "org"), f["user"].(string), roles)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v, nil))
}

// setOrgMemberRoles handles PATCH /orgs/{org}/members/{user}.
func (a *adminAPI) setOrgMemberRoles(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"roles": "strings"})
	if !ok {
		return
	}
	v, err := usersOf(r).AdminSetMemberRoles(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "user"), f["roles"].([]string))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v, nil))
}

// removeOrgMember handles DELETE /orgs/{org}/members/{user}.
func (a *adminAPI) removeOrgMember(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).AdminRemoveMember(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "user")); err != nil {
		adminOrgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setOrgClaims handles PUT /orgs/{org}/claims: the body is the claims object.
func (a *adminAPI) setOrgClaims(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	o, err := usersOf(r).AdminSetClaims(r.Context(), chi.URLParam(r, "org"), obj)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, orgJSON(o))
}

// assignOrgOwner handles POST /orgs/{org}/owner.
func (a *adminAPI) assignOrgOwner(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"user": "string"})
	if !ok {
		return
	}
	v, err := usersOf(r).AdminAssignOwner(r.Context(), chi.URLParam(r, "org"), f["user"].(string))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v, nil))
}
