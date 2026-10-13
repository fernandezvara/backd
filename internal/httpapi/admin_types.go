package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/openapigen"
	"github.com/fernandezvara/backd/internal/typegen"
)

// openapi answers GET /_admin/openapi.json: the OpenAPI document of the admin API and of the
// functions administrators may run by hand, internal ones included.
func (a *adminAPI) openapi(w http.ResponseWriter, r *http.Request) {
	doc, err := openapigen.Admin(a.reg, chi.URLParam(r, "realm"))
	if err != nil {
		notFound(w, r)
		return
	}
	etag := `"` + doc.Fingerprint[:32] + `"`
	w.Header().Set("ETag", etag)
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc.JSON)
}

// types answers GET /_admin/types: the TypeScript types of the config this instance runs, the
// same `backd types` writes from CONFIG_DIR.
func (a *adminAPI) types(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	out, err := typegen.Generate(a.reg, typegen.Options{Realm: realm, Database: r.URL.Query().Get("db"), Config: a.fingerprint})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, err.Error())
		return
	}
	warnings := out.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out.Types, "info": out.Info, "warnings": warnings})
}
