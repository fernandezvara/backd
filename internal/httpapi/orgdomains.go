package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Verified domains (design/sso.md §10): managers of an organization with manage: domains under
// /_orgs/{org}/domains, realm administrators under /_admin/orgs/{org}/domains. A claim is made, the
// DNS record to publish is shown, and a check proves it.

func domainJSON(v auth.DomainView) map[string]any {
	out := map[string]any{
		"domain": v.Domain, "state": v.State, "discoverable": v.Discoverable, "link_accounts": v.LinkAccounts,
		"record":     map[string]any{"type": v.Record.Type, "name": v.Record.Name, "value": v.Record.Value},
		"created_at": formatTime(v.CreatedAt),
	}
	if !v.VerifiedAt.IsZero() {
		out["verified_at"] = formatTime(v.VerifiedAt)
	}
	if !v.LastCheckedAt.IsZero() {
		out["last_checked_at"] = formatTime(v.LastCheckedAt)
	}
	if v.Reason != "" {
		out["reason"] = v.Reason
	}
	return out
}

// domainError maps the errors of the domains to answers; the rest are the organizations'.
func domainError(w http.ResponseWriter, r *http.Request, err error) {
	var refused *auth.DomainRefusedError
	switch {
	case errors.Is(err, auth.ErrDomainsDisabled):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, auth.ErrDomainNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, auth.ErrDomainTaken):
		writeError(w, r, http.StatusConflict, "domain_taken", err.Error())
	case errors.Is(err, auth.ErrDomainExists):
		writeError(w, r, http.StatusConflict, "domain_exists", err.Error())
	case errors.Is(err, auth.ErrDomainsLimit):
		writeError(w, r, http.StatusConflict, "domains_limit", err.Error())
	case errors.As(err, &refused):
		writeError(w, r, http.StatusBadRequest, codeValidation, "that domain can't be claimed", Detail{Path: "domain", Reason: refused.Reason})
	default:
		orgError(w, r, err)
	}
}

func adminDomainError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrNotFound) {
		adminOrgError(w, r, err)
		return
	}
	domainError(w, r, err)
}

func domainList(list []auth.DomainView, max int) map[string]any {
	items := make([]map[string]any, len(list))
	for i, v := range list {
		items[i] = domainJSON(v)
	}
	return map[string]any{"items": items, "max_domains": max}
}

// domainOrg is who and what a handler acts on: the realm's users, the signed-in member and the organization.
func (a *authAPI) domainOrg(_ http.ResponseWriter, r *http.Request) (svc *auth.Users, user, org string) {
	return usersOf(r), principalOf(r).User.ID, chi.URLParam(r, "org")
}

func (a *authAPI) orgDomainList(w http.ResponseWriter, r *http.Request) {
	svc, user, org := a.domainOrg(w, r)
	list, err := svc.ListOrgDomains(r.Context(), user, org)
	if err != nil {
		domainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainList(list, svc.Settings.Organizations.Domains.MaxPerOrg))
}

func (a *authAPI) orgDomainClaim(w http.ResponseWriter, r *http.Request) {
	svc, user, org := a.domainOrg(w, r)
	if err := svc.OrgDomainsAllowed(r.Context(), user, org); err != nil {
		domainError(w, r, err)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"domain": "string"})
	if !ok {
		return
	}
	v, err := svc.ClaimOrgDomain(r.Context(), user, org, f["domain"].(string))
	if err != nil {
		domainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, domainJSON(v))
}

func (a *authAPI) orgDomainVerify(w http.ResponseWriter, r *http.Request) {
	svc, user, org := a.domainOrg(w, r)
	// Checking changes nothing a manager couldn't do by waiting, so it asks for no fresh proof; it is
	// limited per domain instead.
	v, err := svc.VerifyOrgDomain(r.Context(), user, org, chi.URLParam(r, "domain"))
	if err != nil {
		domainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainJSON(v))
}

func domainUpdateBody(w http.ResponseWriter, r *http.Request) (auth.DomainUpdate, bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return auth.DomainUpdate{}, false
	}
	f, ok := fields(w, r, obj, map[string]string{"discoverable": "bool?", "link_accounts": "bool?"})
	if !ok {
		return auth.DomainUpdate{}, false
	}
	var in auth.DomainUpdate
	if b, ok := f["discoverable"].(bool); ok {
		in.Discoverable = &b
	}
	if b, ok := f["link_accounts"].(bool); ok {
		in.LinkAccounts = &b
	}
	return in, true
}

func (a *authAPI) orgDomainUpdate(w http.ResponseWriter, r *http.Request) {
	svc, user, org := a.domainOrg(w, r)
	if err := svc.OrgDomainsAllowed(r.Context(), user, org); err != nil {
		domainError(w, r, err)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	in, ok := domainUpdateBody(w, r)
	if !ok {
		return
	}
	v, err := svc.UpdateOrgDomain(r.Context(), user, org, chi.URLParam(r, "domain"), in)
	if err != nil {
		domainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainJSON(v))
}

func (a *authAPI) orgDomainRemove(w http.ResponseWriter, r *http.Request) {
	svc, user, org := a.domainOrg(w, r)
	if err := svc.OrgDomainsAllowed(r.Context(), user, org); err != nil {
		domainError(w, r, err)
		return
	}
	if !a.reauthed(w, r) {
		return
	}
	if err := svc.RemoveOrgDomain(r.Context(), user, org, chi.URLParam(r, "domain")); err != nil {
		domainError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Administrators.

func (a *adminAPI) orgDomainList(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	list, err := svc.ListOrgDomains(r.Context(), "", chi.URLParam(r, "org"))
	if err != nil {
		adminDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainList(list, svc.Settings.Organizations.Domains.MaxPerOrg))
}

func (a *adminAPI) orgDomainClaim(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"domain": "string"})
	if !ok {
		return
	}
	v, err := usersOf(r).ClaimOrgDomain(r.Context(), "", chi.URLParam(r, "org"), f["domain"].(string))
	if err != nil {
		adminDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, domainJSON(v))
}

func (a *adminAPI) orgDomainVerify(w http.ResponseWriter, r *http.Request) {
	v, err := usersOf(r).VerifyOrgDomain(r.Context(), "", chi.URLParam(r, "org"), chi.URLParam(r, "domain"))
	if err != nil {
		adminDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainJSON(v))
}

func (a *adminAPI) orgDomainUpdate(w http.ResponseWriter, r *http.Request) {
	in, ok := domainUpdateBody(w, r)
	if !ok {
		return
	}
	v, err := usersOf(r).UpdateOrgDomain(r.Context(), "", chi.URLParam(r, "org"), chi.URLParam(r, "domain"), in)
	if err != nil {
		adminDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, domainJSON(v))
}

// orgDomainRemove is also the way to settle a dispute: an administrator releases a claim by force.
func (a *adminAPI) orgDomainRemove(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).RemoveOrgDomain(r.Context(), "", chi.URLParam(r, "org"), chi.URLParam(r, "domain")); err != nil {
		adminDomainError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
