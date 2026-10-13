package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/typegen"
)

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
