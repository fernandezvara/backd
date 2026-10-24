package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Organization sign-in connections (design/org-sso.md §4): managers of an organization with
// manage: sso under /_orgs/{org}/sso, realm administrators under /_admin/orgs/{org}/sso. The
// client secret is accepted and never returned.

// ssoRedirect is the address to register with the provider: backd's public address (the
// realm's email.public_url, else BACKD_URL) and the callback path, which always names the
// organization by its id. Without a public address only the path is known.
func ssoRedirect(reg *registry.Registry, baseURL, realm, orgID, provider string) (uri, path string) {
	path = registry.OrgProviderCallbackPath(realm, orgID, provider)
	base := baseURL
	if rl := reg.Realms[realm]; rl != nil && rl.Settings.Email != nil && rl.Settings.Email.PublicURL != "" {
		base = rl.Settings.Email.PublicURL
	}
	if base == "" {
		return "", path
	}
	return strings.TrimSuffix(base, "/") + path, path
}

func connectionJSON(reg *registry.Registry, baseURL, realm string, v auth.ConnectionView) map[string]any {
	uri, path := ssoRedirect(reg, baseURL, realm, v.Org, v.Provider)
	out := map[string]any{
		"provider": v.Provider, "client_id": v.ClientID, "enabled": v.Enabled,
		"client_secret_set": v.SecretSet,
		"email_domains":     nonNil(v.EmailDomains), "native_client_ids": nonNil(v.NativeClientIDs), "join_roles": nonNil(v.JoinRoles),
		"redirect_path": path,
		"created_at":    formatTime(v.CreatedAt), "updated_at": formatTime(v.UpdatedAt),
	}
	if uri != "" {
		out["redirect_uri"] = uri
	}
	if v.Tenant != "" {
		out["tenant"] = v.Tenant
	}
	if v.HostedDomain != "" {
		out["hosted_domain"] = v.HostedDomain
	}
	return out
}

func connectionList(reg *registry.Registry, baseURL, realm, orgID string, list []auth.ConnectionView, sso *registry.OrgSSOSettings) map[string]any {
	items := make([]map[string]any, len(list))
	for i, v := range list {
		items[i] = connectionJSON(reg, baseURL, realm, v)
	}
	out := map[string]any{"items": items}
	if sso != nil {
		out["providers"] = nonNil(sso.Providers)
		out["max_connections"] = sso.MaxPerOrg
	}
	return out
}

// connectionBody reads the body of a PUT.
func connectionBody(w http.ResponseWriter, r *http.Request) (auth.ConnectionInput, bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return auth.ConnectionInput{}, false
	}
	f, ok := fields(w, r, obj, map[string]string{
		"client_id": "string", "client_secret": "string?", "tenant": "string?", "hosted_domain": "string?",
		"email_domains": "strings?", "native_client_ids": "strings?", "join_roles": "strings?", "enabled": "bool?",
	})
	if !ok {
		return auth.ConnectionInput{}, false
	}
	in := auth.ConnectionInput{}
	in.ClientID, _ = f["client_id"].(string)
	in.ClientSecret, _ = f["client_secret"].(string)
	in.Tenant, _ = f["tenant"].(string)
	in.HostedDomain, _ = f["hosted_domain"].(string)
	in.EmailDomains, _ = f["email_domains"].([]string)
	in.NativeClientIDs, _ = f["native_client_ids"].([]string)
	in.JoinRoles, _ = f["join_roles"].([]string)
	if b, ok := f["enabled"].(bool); ok {
		in.Enabled = &b
	}
	return in, true
}

func (a *authAPI) orgSSOList(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	org := chi.URLParam(r, "org")
	list, err := svc.OrgConnections(r.Context(), principalOf(r).User.ID, org)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionList(a.reg, a.baseURL, chi.URLParam(r, "realm"), org, list, svc.Settings.Organizations.SSO))
}

func (a *authAPI) orgSSOGet(w http.ResponseWriter, r *http.Request) {
	v, err := usersOf(r).OrgConnection(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "provider"))
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(a.reg, a.baseURL, chi.URLParam(r, "realm"), v))
}

func (a *authAPI) orgSSOSet(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	p := principalOf(r)
	org, provider := chi.URLParam(r, "org"), chi.URLParam(r, "provider")
	// Who may and what is valid are checked before asking for a fresh proof, as for a deletion.
	if _, err := svc.OrgConnectionAllowed(r.Context(), p.User.ID, org, provider); err != nil {
		orgError(w, r, err)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	in, ok := connectionBody(w, r)
	if !ok {
		return
	}
	v, err := svc.SetOrgConnection(r.Context(), p.User.ID, org, provider, in)
	if err != nil {
		orgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(a.reg, a.baseURL, chi.URLParam(r, "realm"), v))
}

func (a *authAPI) orgSSODelete(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	p := principalOf(r)
	org, provider := chi.URLParam(r, "org"), chi.URLParam(r, "provider")
	if _, err := svc.OrgConnectionAllowed(r.Context(), p.User.ID, org, provider); err != nil {
		orgError(w, r, err)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	if err := svc.DeleteOrgConnection(r.Context(), p.User.ID, org, provider); err != nil {
		orgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Administrators.

func (a *adminAPI) orgSSOList(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	org := chi.URLParam(r, "org")
	list, err := svc.AdminOrgConnections(r.Context(), org)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionList(a.reg, a.baseURL, chi.URLParam(r, "realm"), org, list, svc.Settings.Organizations.SSO))
}

func (a *adminAPI) orgSSOGet(w http.ResponseWriter, r *http.Request) {
	v, err := usersOf(r).AdminOrgConnection(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "provider"))
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(a.reg, a.baseURL, chi.URLParam(r, "realm"), v))
}

func (a *adminAPI) orgSSOSet(w http.ResponseWriter, r *http.Request) {
	in, ok := connectionBody(w, r)
	if !ok {
		return
	}
	v, err := usersOf(r).AdminSetOrgConnection(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "provider"), in)
	if err != nil {
		adminOrgError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(a.reg, a.baseURL, chi.URLParam(r, "realm"), v))
}

func (a *adminAPI) orgSSODelete(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).AdminDeleteOrgConnection(r.Context(), chi.URLParam(r, "org"), chi.URLParam(r, "provider")); err != nil {
		adminOrgError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
