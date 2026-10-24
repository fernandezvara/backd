package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// systemDataIncluded and systemDataExcluded are what a user's export holds
// besides their documents, and what it never holds (design/compliance.md §3.3).
var systemDataIncluded = []string{
	"account: id, email, verification, locale, roles, created and last sign-in",
	"identities: provider, provider email and subject, linked dates",
	"sessions: created, last used, sign-in methods",
	"push devices: platform, name, created, last seen",
	"MFA: enabled, methods, recovery codes remaining, last used",
	"consents: the full history",
	"audit records where the user is the actor or the subject: event, time and ids",
	"function invocations the user made: function, time and status",
}

var systemDataExcluded = []string{
	"password hashes",
	"provider tokens",
	"session and refresh tokens",
	"device tokens",
	"TOTP secrets and recovery code hashes",
	"function inputs, outputs and logs",
	"other people's details in any of the above",
}

// complianceScope answers GET /_admin/compliance/scope: what a user's data
// export takes from each collection, with the reason for every exclusion, and
// the system data it adds. It changes nothing and holds no data.
func (a *adminAPI) complianceScope(w http.ResponseWriter, r *http.Request) {
	rl, ok := a.reg.Realms[chi.URLParam(r, "realm")]
	if !ok {
		notFound(w, r)
		return
	}
	collections := []map[string]any{}
	for _, e := range rl.PersonalScope() {
		entry := map[string]any{
			"database": e.Database, "collection": e.Collection, "export": e.Export,
			"owned": e.Owned, "pull": orStrings(e.Pull), "files": orStrings(e.Files),
			"declared": e.Declared,
		}
		if e.Fields != nil {
			entry["fields"] = e.Fields
		}
		if e.Reason != "" {
			entry["reason"] = e.Reason
		}
		if e.Organization {
			entry["organization"] = true
		}
		collections = append(collections, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"realm":       rl.Name,
		"auth":        rl.Settings.AuthEnabled,
		"collections": collections,
		"system":      map[string]any{"included": systemDataIncluded, "excluded": systemDataExcluded},
	})
}

// notifyConsent answers POST /_admin/consents/notify {consent}: it queues the
// consent-updated email for the users who hold an older version of the consent. Needs
// the users area with write access; audited as consents.notify.
func (a *adminAPI) notifyConsent(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"consent": "string"})
	if !ok {
		return
	}
	res, err := usersOf(r).NotifyConsentUpdated(r.Context(), f["consent"].(string))
	switch {
	case errors.Is(err, auth.ErrUnknownConsent):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "consent", Reason: "is not a consent of this realm"})
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusConflict, codeConflict, "the realm has no email section: nothing can be sent")
	case err != nil:
		authError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"consent": res.Consent, "version": res.Version, "matched": res.Matched, "queued": res.Queued, "limited": res.Limited})
	}
}

// userConsents answers GET /users/{id}/consents: what the user holds and their whole
// history, with the evidence of the records an administrator made.
func (a *adminAPI) userConsents(w http.ResponseWriter, r *http.Request) {
	svc, u := usersOf(r), adminUserOf(r)
	hist, err := svc.ConsentHistory(r.Context(), u.ID)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(hist))
	for _, rec := range hist {
		item := map[string]any{
			"consent": rec.Consent, "version": rec.Version, "action": rec.Action, "method": rec.Method,
			"locale": rec.Locale, "text_hash": rec.TextHash, "at": formatTime(rec.At),
		}
		if rec.Evidence != "" {
			item["evidence"] = rec.Evidence
		}
		items = append(items, item)
	}
	held := map[string]string{}
	for k, v := range u.Consents {
		held[k] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"consents": held, "history": items})
}

// recordUserConsent answers POST /users/{id}/consents: an administrator records a consent
// the user gave, or withdrew, outside the app, with the evidence for it (method admin).
func (a *adminAPI) recordUserConsent(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"consent": "string", "version": "string?", "action": "string", "evidence": "string"})
	if !ok {
		return
	}
	version, _ := f["version"].(string)
	rec, err := usersOf(r).RecordConsentFor(r.Context(), adminUserOf(r).ID, auth.ConsentInput{
		Consent: f["consent"].(string), Version: version, Action: f["action"].(string), Evidence: f["evidence"].(string),
	})
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"consent": rec.Consent, "version": rec.Version, "action": rec.Action, "method": rec.Method,
		"evidence": rec.Evidence, "at": formatTime(rec.At),
	})
}
