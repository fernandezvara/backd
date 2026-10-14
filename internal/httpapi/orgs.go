package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Organizations (design/organizations.md): /v1/{realm}/_orgs for the signed-in user, and
// /v1/{realm}/_admin/orgs for realm administrators.

const (
	codeNotOrgMember   = "not_org_member"
	codeLastOrgManager = "last_org_manager"
	codeSlugTaken      = "slug_taken"
	codeOrgLimit       = "org_limit_reached"
	codeOrgRequiresMFA = "org_requires_mfa"
)

func (a *authAPI) orgRoutes(r chi.Router) {
	r.Route("/v1/{realm}/_orgs", func(r chi.Router) {
		r.Use(noStore, a.resolveRealm, withTimeout(a.opTimeout), a.requireSession)
		json := requireContentType("application/json")
		r.With(json).Post("/", a.createOrg)
		r.Get("/", a.listOrgs)
		// What an invitation offers, and taking it: the token proves the right to know.
		r.With(json).Post("/invitations/preview", a.previewOrgInvitation)
		r.With(json).Post("/invitations/accept", a.acceptOrgInvitation)
		r.Route("/{org}", func(r chi.Router) {
			r.Use(a.orgMFAGate)
			r.Get("/", a.getOrg)
			r.With(json).Patch("/", a.updateOrg)
			r.Delete("/", a.deleteOrg)
			r.Get("/members", a.orgMembers)
			r.With(json).Patch("/members/{user}", a.updateOrgMember)
			r.Delete("/members/{user}", a.removeOrgMember)
			r.Post("/leave", a.leaveOrg)
			r.Get("/teams", a.listOrgTeams)
			r.With(json).Post("/teams", a.createOrgTeam)
			r.With(json).Patch("/teams/{team}", a.updateOrgTeam)
			r.Delete("/teams/{team}", a.deleteOrgTeam)
			r.Get("/teams/{team}/members", a.orgTeamMembers)
			r.Put("/teams/{team}/members/{user}", a.addOrgTeamMember)
			r.Delete("/teams/{team}/members/{user}", a.removeOrgTeamMember)
			r.With(json).Post("/teams/{team}/roles", a.grantTeamRole(true))
			r.With(json).Delete("/teams/{team}/roles", a.grantTeamRole(false))
			r.Get("/invitations", a.listOrgInvitations)
			r.With(json).Post("/invitations", a.createOrgInvitation)
			r.Delete("/invitations/{id}", a.revokeOrgInvitation)
			r.Post("/invitations/{id}/link", a.newOrgInvitationLink)
			r.Post("/invitations/{id}/send", a.resendOrgInvitation)
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
	case errors.Is(err, auth.ErrOrgRequiresMFA):
		writeError(w, r, http.StatusForbidden, codeOrgRequiresMFA, err.Error())
	case errors.Is(err, auth.ErrRequireMFANotAllowed):
		writeError(w, r, http.StatusConflict, "require_mfa_not_allowed", err.Error())
	case errors.Is(err, auth.ErrOrgForbidden), errors.Is(err, auth.ErrOrgCreateNotAllowed):
		writeError(w, r, http.StatusForbidden, codeForbidden, err.Error())
	case errors.Is(err, auth.ErrOrgLimit), errors.Is(err, auth.ErrMembershipLimit):
		writeError(w, r, http.StatusConflict, codeOrgLimit, err.Error())
	case errors.Is(err, auth.ErrLastOrgManager):
		writeError(w, r, http.StatusConflict, codeLastOrgManager, err.Error())
	case errors.Is(err, auth.ErrSlugTaken):
		writeError(w, r, http.StatusConflict, codeSlugTaken, err.Error(), Detail{Path: "slug", Reason: "is used by another organization"})
	case errors.Is(err, auth.ErrTeamNameTaken):
		writeError(w, r, http.StatusConflict, "team_name_taken", err.Error(), Detail{Path: "name", Reason: "is used by another team of the organization"})
	case errors.Is(err, auth.ErrTeamLimit), errors.Is(err, auth.ErrMemberTeamsLimit), errors.Is(err, auth.ErrTeamRoleLimit):
		writeError(w, r, http.StatusConflict, "team_limit_reached", err.Error())
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, auth.ErrInvalidOrgInvitation):
		writeError(w, r, http.StatusBadRequest, "invalid_invitation", "the invitation is invalid or has expired")
	case errors.Is(err, auth.ErrInvitationNotForYou):
		writeError(w, r, http.StatusForbidden, "invitation_not_for_you", err.Error())
	case errors.Is(err, auth.ErrInvitationsLimit):
		writeError(w, r, http.StatusConflict, "invitations_limit", err.Error())
	case errors.Is(err, auth.ErrNeedsSecretsKey):
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, err.Error())
	case errors.Is(err, auth.ErrOrgNotActive):
		writeError(w, r, http.StatusConflict, "org_not_active", err.Error())
	case errors.Is(err, auth.ErrOrgNotDeleted):
		writeError(w, r, http.StatusConflict, "org_not_deleted", err.Error())
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

func membershipJSON(m auth.Membership, sessionMFA bool) map[string]any {
	out := orgJSON(m.Org)
	// A session without a second factor doesn't count as a member of an organization that
	// requires one: the app uses this to ask for it.
	out["mfa_met"] = !m.Org.RequireMFA || sessionMFA
	out["roles"] = m.RoleNames()
	out["effective_roles"] = roleOriginsJSON(m.Roles)
	out["teams"] = nonNil(m.Member.Teams)
	out["team_roles"] = teamRolesNow(m.TeamRoles)
	return out
}

// teamRolesNow is the team roles in force, by team id: a map even when empty.
func teamRolesNow(by map[string][]string) map[string][]string {
	if by == nil {
		return map[string][]string{}
	}
	return by
}

// teamRoleGrantJSON is one team role grant with its window and who gave it.
func teamRoleGrantJSON(g auth.TeamRoleGrant, now time.Time) map[string]any {
	out := map[string]any{"team": g.Team, "role": g.Role, "granted_at": formatTime(g.GrantedAt), "active": g.Active(now)}
	if !g.From.IsZero() {
		out["from"] = formatTime(g.From)
	}
	if !g.Until.IsZero() {
		out["until"] = formatTime(g.Until)
	}
	if g.GrantedBy != "" {
		out["granted_by"] = g.GrantedBy
	}
	return out
}

func memberJSON(v auth.MemberView) map[string]any {
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
	if v.MFA != nil {
		out["mfa"] = *v.MFA
	}
	grants := make([]map[string]any, len(v.TeamRoles))
	for i, g := range v.TeamRoles {
		grants[i] = teamRoleGrantJSON(g, v.Now)
	}
	out["team_roles"] = grants
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
	writeJSON(w, http.StatusCreated, membershipJSON(m, principalOf(r).Session.MFA))
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
		items[i] = membershipJSON(m, principalOf(r).Session.MFA)
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
	writeJSON(w, http.StatusOK, membershipJSON(m, principalOf(r).Session.MFA))
}

// updateOrg handles PATCH /_orgs/{org}: name and slug.
func (a *authAPI) updateOrg(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string?", "slug": "string?", "require_mfa": "bool?"})
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
	svc, p, org := usersOf(r), principalOf(r), chi.URLParam(r, "org")
	on, setMFA := f["require_mfa"].(bool)
	// Changing what every member must hold is a sensitive act: a fresh proof first.
	if setMFA && !a.reauthed(w, r) {
		return
	}
	m, err := svc.UpdateOrg(r.Context(), p.User.ID, org, name, slug)
	if err != nil {
		orgError(w, r, err)
		return
	}
	if setMFA {
		if m, err = svc.SetOrgRequireMFA(r.Context(), p, org, on); err != nil {
			orgError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, membershipJSON(m, principalOf(r).Session.MFA))
}

// pageParams reads limit and after from the query.
func pageParams(r *http.Request) (limit int, after string) {
	limit = 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 500)
	}
	return limit, r.URL.Query().Get("after")
}

// writePage writes a page of a members list: next is the cursor of the last item.
func writePage(w http.ResponseWriter, items []map[string]any, more bool, last string) {
	out := map[string]any{"items": items, "has_more": more}
	if more && last != "" {
		out["next"] = last
	}
	writeJSON(w, http.StatusOK, out)
}

func memberPage(w http.ResponseWriter, list []auth.MemberView, more bool) {
	items := make([]map[string]any, len(list))
	last := ""
	for i, v := range list {
		items[i], last = memberJSON(v), v.Member.User
	}
	writePage(w, items, more, last)
}

// teamMemberJSON is a member as seen from a team: whether they belong to it and the team roles
// they hold in it now.
func teamMemberJSON(v auth.TeamMemberView) map[string]any {
	out := memberJSON(v.MemberView)
	out["in_team"] = v.InTeam
	out["roles_in_team"] = nonNil(v.Roles)
	return out
}

func teamMemberPage(w http.ResponseWriter, list []auth.TeamMemberView, more bool) {
	items := make([]map[string]any, len(list))
	last := ""
	for i, v := range list {
		items[i], last = teamMemberJSON(v), v.Member.User
	}
	writePage(w, items, more, last)
}

// orgTeamMembers handles GET /_orgs/{org}/teams/{team}/members?role=: the team's members and the
// holders of its roles in force now.
func (a *authAPI) orgTeamMembers(w http.ResponseWriter, r *http.Request) {
	limit, after := pageParams(r)
	list, more, err := usersOf(r).TeamMembers(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team"), r.URL.Query().Get("role"), after, limit)
	if err != nil {
		teamError(w, r, err)
		return
	}
	teamMemberPage(w, list, more)
}

// teamRoleRequest reads the body of a team role request: the member and the role.
func teamRoleRequest(w http.ResponseWriter, r *http.Request) (user, role string, ok bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return "", "", false
	}
	f, ok := fields(w, r, obj, map[string]string{"user": "string", "role": "string"})
	if !ok {
		return "", "", false
	}
	return f["user"].(string), f["role"].(string), true
}

// grantTeamRole handles POST and DELETE /_orgs/{org}/teams/{team}/roles.
func (a *authAPI) grantTeamRole(grant bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, role, ok := teamRoleRequest(w, r)
		if !ok {
			return
		}
		svc, caller, org, team := usersOf(r), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team")
		change := svc.RevokeTeamRole
		if grant {
			change = svc.GrantTeamRole
		}
		v, err := change(r.Context(), caller, org, team, user, role)
		if err != nil {
			teamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, teamMemberJSON(v))
	}
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
	writeJSON(w, http.StatusOK, memberJSON(v))
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
	list, err := usersOf(r).AdminOrgSummaries(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, o := range list {
		items[i] = orgJSON(o.Org)
		items[i]["members"] = o.Members
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
	writeJSON(w, http.StatusCreated, membershipJSON(m, true))
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
	writeJSON(w, http.StatusOK, memberJSON(v))
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
	writeJSON(w, http.StatusOK, memberJSON(v))
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
	writeJSON(w, http.StatusOK, memberJSON(v))
}

func orgInvitationJSON(inv auth.OrgInvitation, now time.Time) map[string]any {
	out := map[string]any{
		"id": inv.ID, "binding": inv.Binding, "status": inv.Status(now),
		"roles": nonNil(inv.Roles), "teams": nonNil(inv.Teams),
		"max_uses": inv.MaxUses, "uses": inv.Uses,
		"created_at": formatTime(inv.CreatedAt), "expires_at": formatTime(inv.ExpiresAt),
	}
	if inv.User != "" {
		out["user"] = inv.User
	}
	if inv.Email != "" {
		out["email"] = inv.Email
	}
	if inv.CreatedBy != "" {
		out["created_by"] = inv.CreatedBy
	}
	return out
}

func invitationResultJSON(res auth.InvitationResult, now time.Time) map[string]any {
	out := orgInvitationJSON(res.Invitation, now)
	out["token"] = res.Token
	if res.Link != "" {
		out["link"] = res.Link
	} else {
		out["link"] = nil
	}
	return out
}

// invitationInput reads the body of a request to invite.
func invitationInput(w http.ResponseWriter, r *http.Request) (auth.InvitationInput, bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return auth.InvitationInput{}, false
	}
	f, ok := fields(w, r, obj, map[string]string{
		"binding": "string", "user": "string?", "email": "string?", "roles": "strings?", "teams": "strings?",
		"send": "bool?", "expires_in": "string?", "max_uses": "int?",
	})
	if !ok {
		return auth.InvitationInput{}, false
	}
	in := auth.InvitationInput{Binding: f["binding"].(string), Locale: r.Header.Get("Accept-Language"), IP: clientIP(r), Request: requestID(r.Context())}
	in.User, _ = f["user"].(string)
	in.Email, _ = f["email"].(string)
	in.Roles, _ = f["roles"].([]string)
	in.Teams, _ = f["teams"].([]string)
	in.Send, _ = f["send"].(bool)
	in.MaxUses, _ = f["max_uses"].(int)
	if s, ok := f["expires_in"].(string); ok {
		d, err := registry.ParseDuration(s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "expires_in", Reason: err.Error()})
			return auth.InvitationInput{}, false
		}
		in.TTL = d
	}
	return in, true
}

// createOrgInvitation handles POST /_orgs/{org}/invitations.
func (a *authAPI) createOrgInvitation(w http.ResponseWriter, r *http.Request) {
	in, ok := invitationInput(w, r)
	if !ok {
		return
	}
	svc := usersOf(r)
	res, err := svc.CreateOrgInvitation(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), in)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, invitationResultJSON(res, svc.Clock()))
}

// listOrgInvitations handles GET /_orgs/{org}/invitations.
func (a *authAPI) listOrgInvitations(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	list, err := svc.OrgInvitations(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeInvitations(w, list, svc.Clock())
}

func writeInvitations(w http.ResponseWriter, list []auth.OrgInvitation, now time.Time) {
	items := make([]map[string]any, len(list))
	for i, inv := range list {
		items[i] = orgInvitationJSON(inv, now)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// revokeOrgInvitation handles DELETE /_orgs/{org}/invitations/{id}.
func (a *authAPI) revokeOrgInvitation(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RevokeOrgInvitation(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "id"))
	if err != nil {
		orgInvitationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// newOrgInvitationLink handles POST /_orgs/{org}/invitations/{id}/link.
func (a *authAPI) newOrgInvitationLink(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	res, err := svc.NewOrgInvitationLink(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "id"))
	if err != nil {
		orgInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitationResultJSON(res, svc.Clock()))
}

// resendOrgInvitation handles POST /_orgs/{org}/invitations/{id}/send.
func (a *authAPI) resendOrgInvitation(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).ResendOrgInvitation(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "id"), r.Header.Get("Accept-Language"), clientIP(r), requestID(r.Context()))
	if err != nil {
		orgInvitationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// orgInvitationError is orgError for routes about one invitation: an unknown one is a 404.
func orgInvitationError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *auth.EmailLimitedError
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "invitation not found")
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case errors.As(err, &limited):
		authError(w, r, err)
	default:
		orgError(w, r, err)
	}
}

// previewOrgInvitation handles POST /_orgs/invitations/preview.
func (a *authAPI) previewOrgInvitation(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"token": "string"})
	if !ok {
		return
	}
	pv, err := usersOf(r).PreviewOrgInvitation(r.Context(), principalOf(r), f["token"].(string))
	if err != nil {
		orgError(w, r, err)
		return
	}
	teams := make([]map[string]any, len(pv.Teams))
	for i, t := range pv.Teams {
		teams[i] = map[string]any{"id": t.ID, "name": t.Name}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"org": map[string]any{"id": pv.Org.ID, "name": pv.Org.Name}, "roles": nonNil(pv.Roles), "teams": teams,
		"binding": pv.Binding, "expires_at": formatTime(pv.ExpiresAt),
	})
}

// acceptOrgInvitation handles POST /_orgs/invitations/accept.
func (a *authAPI) acceptOrgInvitation(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"token": "string"})
	if !ok {
		return
	}
	m, err := usersOf(r).AcceptOrgInvitation(r.Context(), principalOf(r), f["token"].(string))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipJSON(m, principalOf(r).Session.MFA))
}

// Administrators.

func (a *adminAPI) listOrgInvitations(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	list, err := svc.AdminOrgInvitations(r.Context(), chi.URLParam(r, "org"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeInvitations(w, list, svc.Clock())
}

func (a *adminAPI) createOrgInvitation(w http.ResponseWriter, r *http.Request) {
	in, ok := invitationInput(w, r)
	if !ok {
		return
	}
	svc := usersOf(r)
	res, err := svc.AdminCreateOrgInvitation(r.Context(), chi.URLParam(r, "org"), in)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, invitationResultJSON(res, svc.Clock()))
}

func (a *adminAPI) revokeOrgInvitation(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).AdminRevokeOrgInvitation(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "id")); err != nil {
		orgInvitationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) newOrgInvitationLink(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	res, err := svc.AdminNewOrgInvitationLink(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "id"))
	if err != nil {
		orgInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitationResultJSON(res, svc.Clock()))
}

func teamJSON(t auth.OrgTeam) map[string]any {
	return map[string]any{"id": t.ID, "name": t.Name, "roles": nonNil(t.Roles), "created_at": formatTime(t.CreatedAt)}
}

func writeTeams(w http.ResponseWriter, list []auth.OrgTeam) {
	items := make([]map[string]any, len(list))
	for i, t := range list {
		items[i] = teamJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// teamError is orgError for the routes of one team: an unknown team is a 404.
func teamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "team not found")
		return
	}
	orgError(w, r, err)
}

// adminTeamError is teamError for administrators, who are told a member isn't one.
func adminTeamError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "team not found")
		return
	}
	adminOrgError(w, r, err)
}

// listOrgTeams handles GET /_orgs/{org}/teams.
func (a *authAPI) listOrgTeams(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).OrgTeams(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeTeams(w, list)
}

// createOrgTeam handles POST /_orgs/{org}/teams.
func (a *authAPI) createOrgTeam(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "roles": "strings?"})
	if !ok {
		return
	}
	roles, _ := f["roles"].([]string)
	t, err := usersOf(r).CreateOrgTeam(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), f["name"].(string), roles)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, teamJSON(t))
}

func teamPatch(w http.ResponseWriter, r *http.Request) (name *string, roles []string, setRoles, ok bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return nil, nil, false, false
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string?", "roles": "strings?"})
	if !ok {
		return nil, nil, false, false
	}
	if v, has := f["name"].(string); has {
		name = &v
	}
	roles, setRoles = f["roles"].([]string)
	return name, roles, setRoles, true
}

// updateOrgTeam handles PATCH /_orgs/{org}/teams/{team}.
func (a *authAPI) updateOrgTeam(w http.ResponseWriter, r *http.Request) {
	name, roles, set, ok := teamPatch(w, r)
	if !ok {
		return
	}
	t, err := usersOf(r).UpdateOrgTeam(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team"), name, roles, set)
	if err != nil {
		teamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamJSON(t))
}

// deleteOrgTeam handles DELETE /_orgs/{org}/teams/{team}.
func (a *authAPI) deleteOrgTeam(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).DeleteOrgTeam(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team"))
	if err != nil {
		teamError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addOrgTeamMember handles PUT /_orgs/{org}/teams/{team}/members/{user}.
func (a *authAPI) addOrgTeamMember(w http.ResponseWriter, r *http.Request) {
	v, err := usersOf(r).AddOrgTeamMember(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team"), chi.URLParam(r, "user"))
	if err != nil {
		teamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v))
}

// removeOrgTeamMember handles DELETE /_orgs/{org}/teams/{team}/members/{user}.
func (a *authAPI) removeOrgTeamMember(w http.ResponseWriter, r *http.Request) {
	v, err := usersOf(r).RemoveOrgTeamMember(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "team"), chi.URLParam(r, "user"))
	if err != nil {
		teamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberJSON(v))
}

// Administrators.

func (a *adminAPI) listOrgTeams(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).AdminOrgTeams(r.Context(), chi.URLParam(r, "org"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeTeams(w, list)
}

func (a *adminAPI) createOrgTeam(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "roles": "strings?"})
	if !ok {
		return
	}
	roles, _ := f["roles"].([]string)
	t, err := usersOf(r).AdminCreateOrgTeam(r.Context(), chi.URLParam(r, "org"), f["name"].(string), roles)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, teamJSON(t))
}

func (a *adminAPI) updateOrgTeam(w http.ResponseWriter, r *http.Request) {
	name, roles, set, ok := teamPatch(w, r)
	if !ok {
		return
	}
	t, err := usersOf(r).AdminUpdateOrgTeam(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "team"), name, roles, set)
	if err != nil {
		adminTeamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teamJSON(t))
}

func (a *adminAPI) deleteOrgTeam(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).AdminDeleteOrgTeam(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "team")); err != nil {
		adminTeamError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *adminAPI) orgTeamMembers(w http.ResponseWriter, r *http.Request) {
	limit, after := pageParams(r)
	list, more, err := usersOf(r).AdminTeamMembers(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "team"), r.URL.Query().Get("role"), after, limit)
	if err != nil {
		adminTeamError(w, r, err)
		return
	}
	teamMemberPage(w, list, more)
}

func (a *adminAPI) grantTeamRole(grant bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, role, ok := teamRoleRequest(w, r)
		if !ok {
			return
		}
		svc := usersOf(r)
		change := svc.AdminRevokeTeamRole
		if grant {
			change = svc.AdminGrantTeamRole
		}
		v, err := change(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "team"), user, role)
		if err != nil {
			adminTeamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, teamMemberJSON(v))
	}
}

func (a *adminAPI) setOrgTeamMember(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := usersOf(r).AdminSetOrgTeamMember(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "team"), chi.URLParam(r, "user"), add)
		if err != nil {
			adminTeamError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, memberJSON(v))
	}
}

// deleteOrg handles DELETE /_orgs/{org}: it needs manage: org and a fresh proof of identity.
// Access ends at once; a job cleans up the documents.
func (a *authAPI) deleteOrg(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	p := principalOf(r)
	org := chi.URLParam(r, "org")
	m, err := svc.MembershipOf(r.Context(), p.User.ID, org)
	if err != nil {
		orgError(w, r, err)
		return
	}
	if !svc.Settings.Organizations.CapsOf(m.RoleNames()).Has(registry.CapOrg) {
		orgError(w, r, auth.ErrOrgForbidden)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	if _, err := svc.DeleteOrg(r.Context(), p.User.ID, org, requestID(r.Context())); err != nil {
		orgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminDeleteOrg handles DELETE /orgs/{org}.
func (a *adminAPI) deleteOrg(w http.ResponseWriter, r *http.Request) {
	if _, err := usersOf(r).AdminDeleteOrg(r.Context(), chi.URLParam(r, "org"), requestID(r.Context())); err != nil {
		adminOrgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reactivateOrg handles POST /orgs/{org}/reactivate: the organization comes back with no members.
func (a *adminAPI) reactivateOrg(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"slug": "string?"})
	if !ok {
		return
	}
	slug, _ := f["slug"].(string)
	o, err := usersOf(r).AdminReactivateOrg(r.Context(), chi.URLParam(r, "org"), slug)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, orgJSON(o))
}

// orgOwned handles GET /orgs/{org}/owned: what deleting the organization would do, per
// collection, before it is done.
func (a *adminAPI) orgOwned(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	svc := usersOf(r)
	o, err := svc.AdminOrg(r.Context(), chi.URLParam(r, "org"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	rl := a.reg.Realms[realm]
	items := []map[string]any{}
	var toDelete, toKeep int64
	if rl != nil {
		for _, dbName := range sortedKeys(rl.Databases) {
			for _, c := range rl.Databases[dbName].SortedCollections() {
				if !c.InOrganizations() {
					continue
				}
				purger, ok := a.fns.docs.store.Repository(c).(storage.OrgPurger)
				if !ok {
					continue
				}
				n, err := purger.CountInOrg(r.Context(), o.ID)
				if err != nil {
					storageError(w, r, err)
					return
				}
				if c.OnOrgDelete == registry.OrgDelete {
					toDelete += n
				} else {
					toKeep += n
				}
				items = append(items, map[string]any{"database": dbName, "collection": c.Name, "documents": n, "on_org_delete": c.OnOrgDelete})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"org": orgJSON(o), "collections": items, "to_delete": toDelete, "to_keep": toKeep,
	})
}

// orgMFAGate refuses a session without a second factor on the routes of an organization that
// requires one, for its members: leaving stays possible, and everyone else gets the answers
// they got before (a non-member must not learn that the organization exists).
func (a *authAPI) orgMFAGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r)
		if p.Session.MFA || (r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/leave")) {
			next.ServeHTTP(w, r)
			return
		}
		if usersOf(r).OrgNeedsMFA(r.Context(), p.User.ID, chi.URLParam(r, "org")) {
			writeError(w, r, http.StatusForbidden, codeOrgRequiresMFA, auth.ErrOrgRequiresMFA.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}
