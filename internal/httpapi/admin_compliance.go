package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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
		collections = append(collections, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"realm":       rl.Name,
		"auth":        rl.Settings.AuthEnabled,
		"collections": collections,
		"system":      map[string]any{"included": systemDataIncluded, "excluded": systemDataExcluded},
	})
}
