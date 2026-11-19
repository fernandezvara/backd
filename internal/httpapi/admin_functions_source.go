package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// functionSource answers GET /_admin/functions/{database}/{name}/source: the function's code,
// read-only, from the files this instance runs (see registry.Source). It is what an editor in
// the configuration repository changes, so the admin API only shows it.
func (a *adminAPI) functionSource(w http.ResponseWriter, r *http.Request) {
	realm, database, name := chi.URLParam(r, "realm"), chi.URLParam(r, "database"), chi.URLParam(r, "name")
	fn := a.fns.lookup(realm, database, name)
	if fn == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "function not found")
		return
	}
	project := a.reg.Realms[realm].Databases[database].Functions
	src, err := project.ReadSource(fn)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "the function's source could not be read")
		return
	}
	writeJSON(w, http.StatusOK, src)
}
