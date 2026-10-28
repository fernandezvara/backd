package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

// checkStorage answers POST /v1/{realm}/_admin/storage/check: it verifies the
// realm's storage the way a file would use it (credentials, every operation on
// the prefix, the signed SHA-256, signed links, CORS and encryption where the
// provider exposes them) with a few small objects it deletes again. With
// ?storage=public it checks the realm's public storage instead (the headers public
// files are stored with, and a sentinel image it leaves for the operator to open:
// backd never connects to the public url). 200 with the report, whatever it found
// (`ok` says whether anything failed); 404 when the realm has no such storage; 503
// storage_unavailable when its access keys can't be read.
func (a *adminAPI) checkStorage(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	kind := r.URL.Query().Get("storage")
	if kind != "" && kind != "private" && kind != "public" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "storage", Reason: "must be private or public"})
		return
	}
	public := kind == "public"
	obj, err := a.fns.docs.objects.For(r.Context(), realm, public)
	switch {
	case errors.Is(err, errNoStorage):
		section := "storage"
		if public {
			section = "public_storage"
		}
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no "+section+" configured ("+section+": in realm.yaml)")
		return
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	var rep storage.CheckReport
	if public {
		rep = obj.CheckPublic(r.Context(), realm, a.reg.Realms[realm].Settings.PublicStorage.URL, storage.PublicOriginalHeaders)
	} else {
		rep = obj.Check(r.Context(), realm)
	}
	usersOf(r).Audit(r.Context(), auth.AuditStorageCheck, "storage", map[string]any{"ok": rep.OK(), "provider": rep.Provider, "bucket": rep.Bucket, "public": public})
	out := checkReportJSON(rep)
	out["storage"] = map[bool]string{false: "private", true: "public"}[public]
	if public {
		out["sentinel_url"] = rep.SentinelURL
	} else {
		out["sentinel_url"] = nil
	}
	writeJSON(w, http.StatusOK, out)
}

func checkReportJSON(rep storage.CheckReport) map[string]any {
	steps := make([]map[string]any, len(rep.Steps))
	for i, s := range rep.Steps {
		steps[i] = map[string]any{"name": s.Name, "level": s.Level, "detail": s.Detail}
	}
	cors := make([]map[string]any, len(rep.CORS))
	for i, c := range rep.CORS {
		cors[i] = map[string]any{"origins": orStrings(c.Origins), "methods": orStrings(c.Methods), "headers": orStrings(c.Headers)}
	}
	var public any
	if rep.PublicEndpoint != "" {
		public = rep.PublicEndpoint
	}
	return map[string]any{
		"ok": rep.OK(), "provider": rep.Provider, "endpoint": rep.Endpoint, "public_endpoint": public, "bucket": rep.Bucket, "prefix": rep.Prefix,
		"steps": steps, "checksum_sha256": rep.ChecksumSHA256, "encryption": rep.Encryption, "cors": cors, "link_host": rep.LinkHost,
	}
}
