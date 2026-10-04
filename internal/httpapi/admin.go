package httpapi

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// adminAPI serves /v1/{realm}/_admin/…: user management for server-side
// services holding one of the realm's API keys.
type adminAPI struct {
	users func(realm string) *auth.Users
	reg   *registry.Registry
	fns   *functions // runs functions by hand (see adminInvoke)
}

type adminUserKey struct{}

func (a *adminAPI) routes(r chi.Router, resolveRealm func(http.Handler) http.Handler, timeout func(http.Handler) http.Handler) {
	r.Route("/v1/{realm}/_admin", func(r chi.Router) {
		r.Use(noStore, resolveRealm, timeout, a.requireAdmin)
		json := requireContentType("application/json")
		r.Get("/users", a.listUsers)
		r.With(json).Post("/users", a.createUser)
		r.Route("/users/{id}", func(r chi.Router) {
			r.Use(a.loadUser)
			r.Get("/", a.getUser)
			r.Get("/owned", a.owned)
			r.With(json).Patch("/", a.updateUser)
			r.Delete("/", a.deleteUser)
			r.With(json).Post("/password", a.setPassword)
			r.With(json).Post("/email", a.changeEmail)
			r.Put("/roles/{role}", a.addRole)
			r.Delete("/roles/{role}", a.removeRole)
			r.With(json).Put("/networks", a.setNetworks)
		})
		r.Get("/invitations", a.listInvitations)
		r.With(json).Post("/invitations", a.createInvitation)
		r.Delete("/invitations/{id}", a.revokeInvitation)
		r.Get("/apikeys", a.listAPIKeys)
		r.With(json).Post("/apikeys", a.createAPIKey)
		r.Delete("/apikeys/{name}", a.revokeAPIKey)
		r.Get("/secrets", a.listSecrets)
		r.With(json).Put("/secrets/{name}", a.setSecret)
		r.Delete("/secrets/{name}", a.deleteSecret)
		r.Get("/audit", a.listAudit)
		r.Get("/invocations", a.listInvocations)
		r.Get("/jobs", a.listJobs)
		r.With(json).Post("/functions/{database}/{name}/invoke", a.fns.adminInvoke)
	})
}

// requireAdmin admits the realm's admin API keys, acting as themselves,
// and sessions of users holding one of the realm's admin roles.
func (a *adminAPI) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The realm's admin networks come first: outside them the admin
		// API doesn't exist, and credentials aren't even looked at.
		users := usersOf(r)
		if !users.Settings.AdminNetworks.Allows(clientIP(r)) {
			adminRefused(w, r, "outside the realm's admin.allowed_networks")
			return
		}
		if r.Header.Get(onBehalfHeader) != "" {
			writeError(w, r, http.StatusBadRequest, codeInvalidHeader, onBehalfHeader+" is not allowed on admin endpoints")
			return
		}
		credential, ok := bearerToken(r)
		if !ok {
			unauthenticated(w, r, r.Header.Get("Authorization") != "", "an admin API key or an admin session is required")
			return
		}
		caller, err := usersOf(r).Identify(r.Context(), credential)
		if err != nil {
			authError(w, r, err)
			return
		}
		setActor(r.Context(), caller.Actor())
		if !allowedFrom(w, r, caller) {
			return
		}
		allowed := false
		switch {
		case caller.Key != nil:
			allowed = caller.Key.IsAdmin()
		case caller.User != nil:
			allowed = usersOf(r).Settings.IsAdmin(caller.User.User.Roles)
		}
		if !allowed {
			writeError(w, r, http.StatusForbidden, codeForbidden, "admin endpoints need an admin API key or the session of a user with an admin role")
			return
		}
		if caller.User != nil && !caller.User.User.AdminNetworks.Allows(clientIP(r)) {
			adminRefused(w, r, "outside the user's admin_networks")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

func (a *adminAPI) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := usersOf(r).UserByID(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			adminError(w, r, err)
			return
		}
		// An erased user is a tombstone: it can be read, previewed and erased
		// again (to resume), and nothing else.
		if !u.ErasedAt.IsZero() && r.Method != http.MethodGet && r.Method != http.MethodDelete {
			adminError(w, r, auth.ErrUserErased)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminUserKey{}, u)))
	})
}

func adminUserOf(r *http.Request) auth.User { return r.Context().Value(adminUserKey{}).(auth.User) }

func (a *adminAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	q := r.URL.Query()
	var details []Detail
	for k := range q {
		if k != "email" && k != "limit" && k != "skip" && k != "after" {
			details = append(details, Detail{Path: k, Reason: "unknown query parameter"})
		}
	}
	limit, skip := defaultLimit, 0
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		limit = n
	}
	if s := q.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		skip = n
	}
	after := strings.ToLower(strings.TrimSpace(q.Get("after"))) // emails are stored trimmed and lowercased
	if q.Has("after") {
		if q.Has("skip") {
			details = append(details, Detail{Path: "after", Reason: "can't be combined with skip: a cursor already says where the page starts"})
		}
		if after == "" {
			details = append(details, Detail{Path: "after", Reason: "must not be empty: use the next_cursor of a previous page"})
		}
	}
	if len(details) > 0 {
		sort.Slice(details, func(i, j int) bool { return details[i].Path < details[j].Path })
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}

	var users []auth.User
	hasMore, listed := false, false
	if email := q.Get("email"); email != "" {
		// Exact lookup, e.g. to turn an email into an id for member lists.
		u, err := svc.Find(r.Context(), email)
		switch {
		case err == nil:
			users = []auth.User{u}
		case !errors.Is(err, auth.ErrNotFound):
			adminError(w, r, err)
			return
		}
		page := users[min(skip, len(users)):]
		hasMore = len(page) > limit
		users = page[:min(limit, len(page))]
	} else {
		// One page from the database: never the whole collection.
		var err error
		if users, hasMore, err = svc.ListPage(r.Context(), after, skip, limit); err != nil {
			adminError(w, r, err)
			return
		}
		listed = true
	}
	items := make([]map[string]any, len(users))
	for i, u := range users {
		items[i] = adminUserJSON(u, usersOf(r).LocaleOf(u))
	}
	resp := map[string]any{"items": items, "limit": limit, "skip": skip, "has_more": hasMore}
	if listed && hasMore && len(users) > 0 {
		// Emails are unique and the list is sorted by them: the last one is the position.
		resp["next_cursor"] = users[len(users)-1].Email
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *adminAPI) createUser(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "password": "string?"})
	if !ok {
		return
	}
	var pw *string
	if p, ok := f["password"].(string); ok {
		pw = &p
	}
	svc := usersOf(r)
	email := f["email"].(string)
	if _, err := registry.NormalizeEmail(email); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	u, err := svc.Create(r.Context(), email, pw)
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminUserJSON(u, usersOf(r).LocaleOf(u)))
}

func (a *adminAPI) getUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adminUserJSON(adminUserOf(r), usersOf(r).LocaleOf(adminUserOf(r))))
}

func (a *adminAPI) updateUser(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	if _, has := obj["email"]; has {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body",
			Detail{Path: "email", Reason: "can't be changed through backd"})
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email_verified": "bool?", "disabled": "bool?"})
	if !ok {
		return
	}
	svc, u := usersOf(r), adminUserOf(r)
	if v, ok := f["email_verified"].(bool); ok {
		if err := svc.SetEmailVerified(r.Context(), u.Email, v); err != nil {
			adminError(w, r, err)
			return
		}
	}
	if v, ok := f["disabled"].(bool); ok {
		if err := svc.SetDisabled(r.Context(), u.Email, v); err != nil {
			adminError(w, r, err)
			return
		}
	}
	a.writeUser(w, r, u.ID)
}

// deleteUser handles DELETE /users/{id}: it erases the user. The user is a
// tombstone at once and their sessions and sign-in methods are gone; a worker
// applies the collections' policies, and the answer is the job doing it.
func (a *adminAPI) deleteUser(w http.ResponseWriter, r *http.Request) {
	job, err := usersOf(r).Erase(r.Context(), adminUserOf(r).ID, requestID(r.Context()))
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": job.Status})
}

func (a *adminAPI) setPassword(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"password": "string"})
	if !ok {
		return
	}
	if err := usersOf(r).SetPassword(r.Context(), adminUserOf(r).Email, f["password"].(string)); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// changeEmail handles POST /users/{id}/email: an administrator changes a
// user's address at once. Both addresses are told, the old one with a link to
// undo it. It needs the realm to send email.
func (a *adminAPI) changeEmail(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string"})
	if !ok {
		return
	}
	address := f["email"].(string)
	if _, err := registry.NormalizeEmail(address); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	u, err := usersOf(r).ChangeEmailAsAdmin(r.Context(), adminUserOf(r).ID, address)
	switch {
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm doesn't send email")
	case errors.Is(err, auth.ErrSameEmail):
		writeError(w, r, http.StatusBadRequest, codeValidation, err.Error(), Detail{Path: "email", Reason: "is already the address of this user"})
	case err != nil:
		adminError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, adminUserJSON(u, usersOf(r).LocaleOf(u)))
	}
}

func (a *adminAPI) addRole(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).AddRole(r.Context(), adminUserOf(r).Email, chi.URLParam(r, "role")); err != nil {
		adminError(w, r, err)
		return
	}
	a.writeUser(w, r, adminUserOf(r).ID)
}

func (a *adminAPI) removeRole(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).RemoveRole(r.Context(), adminUserOf(r).Email, chi.URLParam(r, "role")); err != nil {
		adminError(w, r, err)
		return
	}
	a.writeUser(w, r, adminUserOf(r).ID)
}

func (a *adminAPI) createInvitation(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string?", "expires_in": "string?", "send": "bool?", "redirect_to": "string?", "locale": "string?"})
	if !ok {
		return
	}
	email, _ := f["email"].(string)
	var ttl time.Duration
	if s, ok := f["expires_in"].(string); ok {
		d, err := registry.ParseDuration(s)
		if err != nil || d <= 0 {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "expires_in", Reason: "must be a duration such as 7d or 12h"})
			return
		}
		ttl = d
	}
	if email != "" {
		if _, err := registry.NormalizeEmail(email); err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "email", Reason: "must be a valid email address"})
			return
		}
	}
	caller, _ := callerOf(r)
	if send, _ := f["send"].(bool); send {
		a.sendInvitation(w, r, f, email, ttl, caller.Actor())
		return
	}
	for _, k := range []string{"redirect_to", "locale"} {
		if _, given := f[k]; given {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: k, Reason: "only applies with send: true"})
			return
		}
	}
	inv, token, err := usersOf(r).CreateInvitation(r.Context(), email, ttl, caller.Actor())
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "expires_in", Reason: err.Error()})
		return
	}
	out := invitationJSON(inv)
	out["token"] = token
	writeJSON(w, http.StatusCreated, out)
}

// sendInvitation creates an invitation that backd emails to its address.
func (a *adminAPI) sendInvitation(w http.ResponseWriter, r *http.Request, f map[string]any, address string, ttl time.Duration, createdBy string) {
	redirectTo, _ := f["redirect_to"].(string)
	locale, _ := f["locale"].(string)
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	if redirectTo != "" && (rl == nil || rl.Settings.Email == nil || !rl.Settings.Email.AllowedRedirect(redirectTo)) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to must be an absolute URL within the realm's email.allowed_redirects", Detail{Path: "redirect_to", Reason: "is not allowed"})
		return
	}
	inv, err := usersOf(r).SendInvitation(r.Context(), address, ttl, createdBy, redirectTo, locale, clientIP(r), requestID(r.Context()))
	switch {
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm doesn't send email")
	case errors.Is(err, auth.ErrInvitationNeedsAddress):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "email", Reason: "is required to send an invitation"})
	case err != nil:
		var limited *auth.EmailLimitedError
		if errors.As(err, &limited) {
			authError(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "expires_in", Reason: err.Error()})
	default:
		out := invitationJSON(inv)
		out["sent"] = true
		writeJSON(w, http.StatusCreated, out)
	}
}

func (a *adminAPI) listInvitations(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).Invitations(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, inv := range list {
		items[i] = invitationJSON(inv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *adminAPI) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RevokeInvitation(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "invitation not found")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func invitationJSON(inv auth.Invitation) map[string]any {
	var email any
	if inv.Email != "" {
		email = inv.Email
	}
	return map[string]any{
		"id":         inv.ID,
		"email":      email,
		"created_by": inv.CreatedBy,
		"created_at": formatTime(inv.CreatedAt),
		"expires_at": formatTime(inv.ExpiresAt),
	}
}

// writeUser answers with the user's current state.
func (a *adminAPI) writeUser(w http.ResponseWriter, r *http.Request, id string) {
	u, err := usersOf(r).UserByID(r.Context(), id)
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminUserJSON(u, usersOf(r).LocaleOf(u)))
}

// fields checks a JSON object against a spec of field name → kind
// ("string", "bool", with "?" when optional) and rejects unknown fields.
func fields(w http.ResponseWriter, r *http.Request, obj map[string]any, spec map[string]string) (map[string]any, bool) {
	out := map[string]any{}
	var details []Detail
	for _, name := range slices.Sorted(maps.Keys(spec)) {
		kind := spec[name]
		optional := kind[len(kind)-1] == '?'
		if optional {
			kind = kind[:len(kind)-1]
		}
		v, has := obj[name]
		switch {
		case !has || v == nil:
			if !optional {
				details = append(details, Detail{Path: name, Reason: "is required"})
			}
		case kind == "string":
			if s, ok := v.(string); ok {
				out[name] = s
			} else {
				details = append(details, Detail{Path: name, Reason: "must be a string"})
			}
		case kind == "bool":
			if b, ok := v.(bool); ok {
				out[name] = b
			} else {
				details = append(details, Detail{Path: name, Reason: "must be a boolean"})
			}
		case kind == "strings":
			arr, ok := v.([]any)
			list := make([]string, 0, len(arr))
			for _, e := range arr {
				if s, isStr := e.(string); isStr {
					list = append(list, s)
				} else {
					ok = false
				}
			}
			if ok {
				out[name] = list
			} else {
				details = append(details, Detail{Path: name, Reason: "must be an array of strings"})
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if _, known := spec[k]; !known {
			details = append(details, Detail{Path: k, Reason: "is not allowed"})
		}
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return nil, false
	}
	return out, true
}

func adminError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *auth.PolicyError
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "user not found")
	case errors.Is(err, auth.ErrUserErased):
		writeError(w, r, http.StatusConflict, codeUserErased, "this user was erased: only a tombstone is left")
	case errors.Is(err, auth.ErrAlreadyErased):
		writeError(w, r, http.StatusConflict, codeAlreadyErased, err.Error())
	case errors.Is(err, auth.ErrUndeclaredRole):
		writeError(w, r, http.StatusBadRequest, codeValidation, err.Error(), Detail{Path: "role", Reason: "is not declared in realm.yaml"})
	case errors.As(err, &pe):
		writeError(w, r, http.StatusBadRequest, codeValidation, "password does not meet the policy", Detail{Path: "password", Reason: pe.Reason})
	case errors.Is(err, auth.ErrSecretsNotConfigured):
		writeError(w, r, http.StatusInternalServerError, codeInternal, err.Error())
	default:
		authError(w, r, err)
	}
}

func adminUserJSON(u auth.User, locale string) map[string]any {
	out := userJSON(u, locale)
	out["disabled"] = u.Disabled
	out["admin_networks"] = u.AdminNetworks.Strings()
	out["login_networks"] = u.LoginNetworks.Strings()
	out["updated_at"] = formatTime(u.UpdatedAt)
	out["erased_at"] = nil
	if !u.ErasedAt.IsZero() {
		out["erased_at"] = formatTime(u.ErasedAt)
	}
	return out
}

// networksField parses a list of networks from a request body.
func networksField(w http.ResponseWriter, r *http.Request, name string, list []string) (registry.Networks, bool) {
	n, err := registry.ParseNetworks(list)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: name, Reason: err.Error()})
		return nil, false
	}
	return n, true
}

// setNetworks replaces a user's network restrictions.
func (a *adminAPI) setNetworks(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"admin_networks": "strings", "login_networks": "strings"})
	if !ok {
		return
	}
	admin, ok := networksField(w, r, "admin_networks", f["admin_networks"].([]string))
	if !ok {
		return
	}
	login, ok := networksField(w, r, "login_networks", f["login_networks"].([]string))
	if !ok {
		return
	}
	u, err := usersOf(r).SetNetworks(r.Context(), adminUserOf(r).Email, admin, login)
	if errors.Is(err, auth.ErrNetworksOutsideRealm) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "admin_networks", Reason: "must lie within the realm's admin.allowed_networks"})
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminUserJSON(u, usersOf(r).LocaleOf(u)))
}

func apiKeyJSON(k auth.APIKey) map[string]any {
	role := k.Role
	if role == "" {
		role = auth.KeyRoleData
	}
	var lastUsed, expires any
	if k.LastUsedAt != nil {
		lastUsed = formatTime(*k.LastUsedAt)
	}
	if k.ExpiresAt != nil {
		expires = formatTime(*k.ExpiresAt)
	}
	return map[string]any{
		"name":         k.Name,
		"role":         string(role),
		"prefix":       k.Prefix,
		"networks":     k.Networks.Strings(),
		"scopes":       k.Scopes.Strings(),
		"created_at":   formatTime(k.CreatedAt),
		"last_used_at": lastUsed,
		"expires_at":   expires,
	}
}

func (a *adminAPI) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := usersOf(r).ListAPIKeys(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(keys))
	for i, k := range keys {
		items[i] = apiKeyJSON(k)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// createAPIKey creates a key; the key itself is in this response only.
func (a *adminAPI) createAPIKey(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "role": "string?", "expires_in": "string?", "networks": "strings?", "scopes": "strings?"})
	if !ok {
		return
	}
	var opts auth.KeyOptions
	if s, ok := f["role"].(string); ok {
		role, err := auth.ParseKeyRole(s)
		if err != nil || s == "" {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "role", Reason: "must be data or admin"})
			return
		}
		opts.Role = role
	}
	if s, ok := f["expires_in"].(string); ok {
		d, err := registry.ParseDuration(s)
		if err != nil || d <= 0 {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "expires_in", Reason: "must be a duration such as 90d or 12h"})
			return
		}
		opts.TTL = d
	}
	if list, ok := f["networks"].([]string); ok {
		if opts.Networks, ok = networksField(w, r, "networks", list); !ok {
			return
		}
	}
	if list, ok := f["scopes"].([]string); ok {
		if opts.Scopes, ok = a.scopesField(w, r, list); !ok {
			return
		}
	}
	k, key, err := usersOf(r).CreateAPIKey(r.Context(), f["name"].(string), opts)
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "scopes limit data keys"):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "scopes", Reason: err.Error()})
		return
	case errors.Is(err, auth.ErrKeyNameTaken):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error(), Detail{Path: "name", Reason: "must be unique"})
		return
	case err != nil && strings.HasPrefix(err.Error(), "invalid API key name"):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "name", Reason: err.Error()})
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	out := apiKeyJSON(k)
	out["key"] = key
	writeJSON(w, http.StatusCreated, out)
}

// scopesField parses the scopes of a new key and checks that each names
// something the realm has, so a typo can't make a key quietly useless (or, for
// a key meant to be narrow, quietly pointing at nothing).
func (a *adminAPI) scopesField(w http.ResponseWriter, r *http.Request, list []string) (auth.Scopes, bool) {
	realm := chi.URLParam(r, "realm")
	var details []Detail
	var out auth.Scopes
	for i, s := range list {
		path := "scopes[" + strconv.Itoa(i) + "]"
		sc, err := auth.ParseScope(s)
		if err != nil {
			details = append(details, Detail{Path: path, Reason: err.Error()})
			continue
		}
		if reason := a.scopeTarget(realm, sc); reason != "" {
			details = append(details, Detail{Path: path, Reason: reason})
			continue
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	if len(list) > auth.MaxScopes {
		details = append(details, Detail{Path: "scopes", Reason: "at most " + strconv.Itoa(auth.MaxScopes) + " scopes per key"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return nil, false
	}
	return out, true
}

// scopeTarget says why a grant names nothing the realm has ("" when it does).
func (a *adminAPI) scopeTarget(realm string, sc auth.Scope) string {
	if sc.Database == "" {
		return ""
	}
	rl := a.reg.Realms[realm]
	db := rl.Databases[sc.Database]
	switch {
	case db == nil:
		return "the realm has no database " + sc.Database
	case sc.Op == auth.ScopeCall && sc.Name == "":
		if db.Functions == nil {
			return "the database " + sc.Database + " has no functions"
		}
	case sc.Op == auth.ScopeCall:
		fn := (*registry.Function)(nil)
		if db.Functions != nil {
			fn = db.Functions.Functions[sc.Name]
		}
		switch {
		case fn == nil:
			return "the database " + sc.Database + " has no function " + sc.Name
		case fn.Internal:
			return sc.Name + " is an internal function: it has no HTTP route to call"
		}
	case sc.Name != "":
		if _, ok := db.Collections[sc.Name]; !ok {
			return "the database " + sc.Database + " has no collection " + sc.Name
		}
	}
	return ""
}

func (a *adminAPI) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RevokeAPIKey(r.Context(), chi.URLParam(r, "name"))
	if errors.Is(err, auth.ErrKeyNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "API key not found")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// secretMetaJSON never includes a value: secrets are write-only through
// this API.
func secretMetaJSON(m auth.SecretMeta) map[string]any {
	return map[string]any{
		"database":   m.Database,
		"name":       m.Name,
		"created_at": formatTime(m.CreatedAt),
		"updated_at": formatTime(m.UpdatedAt),
		"updated_by": m.UpdatedBy,
	}
}

func (a *adminAPI) listSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := usersOf(r).ListSecrets(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(secrets))
	for i, s := range secrets {
		items[i] = secretMetaJSON(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// knownDatabase reports whether database belongs to realm, per the
// loaded config.
func (a *adminAPI) knownDatabase(realm, database string) bool {
	rl, ok := a.reg.Realms[realm]
	if !ok {
		return false
	}
	_, ok = rl.Databases[database]
	return ok
}

// setSecret creates or replaces a secret's value: database scope (its
// own database) with "database", realm scope without it.
func (a *adminAPI) setSecret(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"value": "string", "database": "string?"})
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	if !registry.ValidSecretName(name) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "name", Reason: "must be upper-case letters, digits and _, starting with a letter"})
		return
	}
	value, _ := f["value"].(string)
	if value == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "value", Reason: "must not be empty"})
		return
	}
	database, _ := f["database"].(string)
	if database != "" && !a.knownDatabase(chi.URLParam(r, "realm"), database) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "database", Reason: "is not a database of this realm"})
		return
	}
	caller, _ := callerOf(r)
	if err := usersOf(r).SetSecret(r.Context(), database, name, value, caller.Actor()); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSecret removes a secret's value; ?database= selects the database
// scope, absent means the realm scope. A function that declares it
// starts answering secret_missing again.
func (a *adminAPI) deleteSecret(w http.ResponseWriter, r *http.Request) {
	database := r.URL.Query().Get("database")
	err := usersOf(r).DeleteSecret(r.Context(), database, chi.URLParam(r, "name"))
	if errors.Is(err, auth.ErrSecretNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "secret not found")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminRefused answers an admin request from a network that isn't allowed
// with 404, as if the admin API didn't exist, and logs it.
func adminRefused(w http.ResponseWriter, r *http.Request, reason string) {
	logger(r.Context()).Warn("admin request refused by network", "realm", chi.URLParam(r, "realm"), "client", clientIP(r), "reason", reason)
	usersOf(r).Audit(r.Context(), auth.AuditAdminRefused, "", map[string]any{"reason": reason, "method": r.Method, "path": r.URL.Path})
	notFound(w, r)
}

// listAudit reads the realm's audit trail, newest first. Nothing can
// change or delete records through the API.
func (a *adminAPI) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auth.AuditFilter{Action: q.Get("action"), Actor: q.Get("actor"), Target: q.Get("target"), Limit: defaultLimit}
	var details []Detail
	for k := range q {
		if !slices.Contains([]string{"action", "actor", "target", "since", "until", "limit", "skip"}, k) {
			details = append(details, Detail{Path: k, Reason: "unknown query parameter"})
		}
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(t.name); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				details = append(details, Detail{Path: t.name, Reason: "must be an RFC 3339 date-time"})
			}
			*t.dst = v
		}
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		f.Limit = n
	}
	if s := q.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		f.Skip = n
	}
	if len(details) > 0 {
		sort.Slice(details, func(i, j int) bool { return details[i].Path < details[j].Path })
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}
	recs, more, err := usersOf(r).AuditTrail(r.Context(), f)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(recs))
	for i, rec := range recs {
		items[i] = auditJSON(rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": f.Limit, "skip": f.Skip, "has_more": more})
}

// listInvocations reads the realm's function invocation history, newest
// first. Never includes a function's input or output — only what
// happened and its own console log lines.
func (a *adminAPI) listInvocations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auth.InvocationFilter{Function: q.Get("function"), RequestID: q.Get("request_id"), Limit: defaultLimit}
	var details []Detail
	for k := range q {
		if !slices.Contains([]string{"function", "request_id", "since", "until", "limit", "skip"}, k) {
			details = append(details, Detail{Path: k, Reason: "unknown query parameter"})
		}
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(t.name); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				details = append(details, Detail{Path: t.name, Reason: "must be an RFC 3339 date-time"})
			}
			*t.dst = v
		}
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		f.Limit = n
	}
	if s := q.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		f.Skip = n
	}
	if len(details) > 0 {
		sort.Slice(details, func(i, j int) bool { return details[i].Path < details[j].Path })
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}
	recs, more, err := usersOf(r).Invocations(r.Context(), f)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(recs))
	for i, rec := range recs {
		items[i] = invocationJSON(rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": f.Limit, "skip": f.Skip, "has_more": more})
}

// listJobs answers GET /v1/{realm}/_admin/jobs: async and scheduled jobs of
// the realm, newest first, without their input or output (those stay at
// GET .../_jobs/{id}, for the caller who enqueued a job or an API key).
func (a *adminAPI) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auth.JobFilter{Status: q.Get("status"), Origin: q.Get("origin"), Limit: defaultLimit}
	var details []Detail
	for k := range q {
		if !slices.Contains([]string{"function", "status", "origin", "scheduled", "since", "until", "limit", "skip"}, k) {
			details = append(details, Detail{Path: k, Reason: "unknown query parameter"})
		}
	}
	if s := q.Get("function"); s != "" {
		database, name, ok := strings.Cut(s, "/")
		if !ok || database == "" || name == "" {
			details = append(details, Detail{Path: "function", Reason: "must be <database>/<name>"})
		}
		f.Database, f.Function = database, name
	}
	if f.Status != "" && !slices.Contains([]string{auth.JobQueued, auth.JobRunning, auth.JobDone}, f.Status) {
		details = append(details, Detail{Path: "status", Reason: "must be queued, running or done"})
	}
	if s := q.Get("scheduled"); s != "" {
		v, err := strconv.ParseBool(s)
		if err != nil {
			details = append(details, Detail{Path: "scheduled", Reason: "must be true or false"})
		}
		f.Scheduled = &v
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(t.name); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				details = append(details, Detail{Path: t.name, Reason: "must be an RFC 3339 date-time"})
			}
			*t.dst = v
		}
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		f.Limit = n
	}
	if s := q.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		f.Skip = n
	}
	if len(details) > 0 {
		sort.Slice(details, func(i, j int) bool { return details[i].Path < details[j].Path })
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}
	jobs, more, err := usersOf(r).Jobs(r.Context(), f)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(jobs))
	for i, j := range jobs {
		items[i] = jobSummaryJSON(j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": f.Limit, "skip": f.Skip, "has_more": more})
}

// jobSummaryJSON is a job as the admin listing shows it: state and how it
// ended, never its input or output.
func jobSummaryJSON(j auth.Job) map[string]any {
	out := map[string]any{
		"id":              j.ID,
		"function":        j.Database + "/" + j.Function,
		"status":          j.Status,
		"scheduled":       j.Scheduled,
		"origin":          j.Origin,
		"email_kind":      nil,
		"attempts":        j.Attempts,
		"created_at":      formatTime(j.CreatedAt),
		"completed_at":    nil,
		"next_attempt_at": nil,
		"result":          nil,
	}
	if j.Email != nil {
		out["email_kind"] = j.Email.Kind // never the recipients or the message
	}
	if !j.NextAttemptAt.IsZero() && j.Status != auth.JobDone {
		out["next_attempt_at"] = formatTime(j.NextAttemptAt)
	}
	if !j.CompletedAt.IsZero() {
		out["completed_at"] = formatTime(j.CompletedAt)
	}
	if j.Status == auth.JobDone && j.Result != nil {
		res := jobResultJSON(j)
		summary := map[string]any{"status": res["status"], "code": nil, "duration_ms": j.Result.DurationMS}
		if code, ok := res["code"]; ok {
			summary["code"] = code
		}
		out["result"] = summary
	}
	return out
}

func invocationJSON(rec auth.InvocationRecord) map[string]any {
	logs := make([]map[string]any, len(rec.Logs))
	for i, l := range rec.Logs {
		logs[i] = map[string]any{"level": l.Level, "line": l.Line}
	}
	out := map[string]any{
		"id":          rec.ID,
		"at":          formatTime(rec.At),
		"function":    rec.Function,
		"actor":       rec.Actor,
		"mode":        rec.Mode,
		"status":      rec.Status,
		"code":        nil,
		"duration_ms": rec.DurationMS,
		"request_id":  nil,
		"job_id":      nil,
		"parent_id":   nil,
		"origin":      nil,
		"logs":        logs,
	}
	for k, v := range map[string]string{"code": rec.Code, "request_id": rec.RequestID, "job_id": rec.JobID, "parent_id": rec.ParentID, "origin": rec.Origin} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func auditJSON(rec auth.AuditRecord) map[string]any {
	details := rec.Details
	if details == nil {
		details = map[string]any{}
	}
	out := map[string]any{
		"id":         rec.ID,
		"at":         formatTime(rec.At),
		"action":     rec.Action,
		"actor":      rec.Actor,
		"target":     nil,
		"details":    details,
		"request_id": nil,
		"client_ip":  nil,
	}
	for k, v := range map[string]string{"target": rec.Target, "request_id": rec.RequestID, "client_ip": rec.ClientIP} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}
