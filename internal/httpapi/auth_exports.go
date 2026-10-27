package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// exportLinkTTL is how long the signed link to a package works: long enough to start the
// download, short enough that a copied link is of no use later.
const exportLinkTTL = 5 * time.Minute

// exportRoutes registers the user's own exports (design/compliance.md §4.1): asking for one,
// listing them and downloading a ready one. They are inside the group that needs a session.
func (a *authAPI) exportRoutes(r chi.Router) {
	r.Post("/me/export", a.requestExport)
	r.Get("/me/exports", a.listExports)
	r.Get("/me/exports/{id}", a.getExport)
	r.Get("/me/exports/{id}/download", a.downloadExport)
}

// exportsEnabled answers 404 unless the realm exports its users' data.
func (a *authAPI) exportsEnabled(w http.ResponseWriter, r *http.Request) (*registry.Realm, bool) {
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	if rl == nil || !rl.Settings.Compliance.Export {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm does not export its users' data (compliance.export in realm.yaml)")
		return nil, false
	}
	return rl, true
}

// requestExport handles POST /_auth/me/export: with a fresh re-authentication, it queues the
// export of the signed-in user's data, at most one a day.
func (a *authAPI) requestExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.exportsEnabled(w, r); !ok {
		return
	}
	if !a.accountReauthed(w, r) {
		return
	}
	svc, p := usersOf(r), principalOf(r)
	rec, err := svc.RequestUserExport(r.Context(), p.User.ID, auth.ExportRequest{RequestID: requestID(r.Context())})
	var soon *auth.ErrExportTooSoon
	switch {
	case errors.As(err, &soon):
		retryAfter(w, soon.RetryAfter)
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, soon.Error())
		return
	case errors.Is(err, auth.ErrExportRunning):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
		return
	case err != nil:
		authError(w, r, err)
		return
	}
	svc.Audit(r.Context(), auth.AuditDataExportRequested, "user:"+p.User.ID, map[string]any{"export": rec.ID})
	writeJSON(w, http.StatusAccepted, exportJSON(rec, false))
}

// listExports handles GET /_auth/me/exports: the user's own exports, newest first. The ones an
// administrator made for them are not theirs to download.
func (a *authAPI) listExports(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.exportsEnabled(w, r); !ok {
		return
	}
	list, err := usersOf(r).UserExports(r.Context(), principalOf(r).User.ID)
	if err != nil {
		authError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, e := range list {
		if !e.ByAdmin() {
			items = append(items, exportJSON(e, false))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// getExport handles GET /_auth/me/exports/{id}, for a page that waits for its export.
func (a *authAPI) getExport(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.ownExport(w, r)
	if !ok {
		return
	}
	out := exportJSON(rec, false)
	if rec.Status == auth.ExportQueued || rec.Status == auth.ExportRunning {
		if job, found, err := usersOf(r).GetJob(r.Context(), rec.JobID); err == nil && found && len(job.Steps) > 0 {
			out["step"] = job.Steps[len(job.Steps)-1].Name
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ownExport finds the export of the URL among the signed-in user's own (another user's, an
// administrator's and a missing one are all not found).
func (a *authAPI) ownExport(w http.ResponseWriter, r *http.Request) (auth.UserExport, bool) {
	if _, ok := a.exportsEnabled(w, r); !ok {
		return auth.UserExport{}, false
	}
	rec, err := usersOf(r).UserExport(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "id"))
	if errors.Is(err, auth.ErrNotFound) || (err == nil && rec.ByAdmin()) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such export")
		return auth.UserExport{}, false
	}
	if err != nil {
		authError(w, r, err)
		return auth.UserExport{}, false
	}
	return rec, true
}

// downloadExport handles GET /_auth/me/exports/{id}/download. With the realm's files delivered
// by signed link it redirects to a link that works for a few minutes (or, when the caller asks
// for JSON, says it: a page that fetches the API cannot follow a redirect to another origin);
// with files proxied it sends the package itself.
func (a *authAPI) downloadExport(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.ownExport(w, r)
	if !ok {
		return
	}
	svc, p := usersOf(r), principalOf(r)
	realm := chi.URLParam(r, "realm")
	switch {
	case rec.Status != auth.ExportReady || rec.Key == "":
		writeError(w, r, http.StatusConflict, codeConflict, "this export is "+rec.Status+": there is nothing to download")
		return
	case !rec.ExpiresAt.After(svc.Clock()):
		writeError(w, r, http.StatusGone, "gone", "this export has expired; ask for a new one")
		return
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm)
	if err != nil {
		linkError(w, r, err)
		return
	}
	name := realm + "-your-data-" + rec.CreatedAt.UTC().Format("2006-01-02") + ".zip"
	st := a.reg.Realms[realm].Settings.Storage
	proxied := st != nil && st.Download == registry.DownloadProxy
	var link storage.Link
	if !proxied {
		if link, err = obj.PresignGet(r.Context(), rec.Key, exportLinkTTL, name, "application/zip"); err != nil {
			linkError(w, r, err)
			return
		}
	}
	// Counted and audited once the link is given, or the bytes are on their way.
	rec.Downloads++
	if err := svc.Store.ReplaceUserExport(r.Context(), rec); err != nil {
		logger(r.Context()).Error("count an export download", "export", rec.ID, "error", err)
	}
	svc.Audit(r.Context(), auth.AuditDataExportDownloaded, "user:"+p.User.ID, map[string]any{"export": rec.ID, "by": "user"})

	if proxied {
		sendExportPackage(w, r, obj, rec.Key, name)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"url": link.URL, "expires_at": formatTime(link.ExpiresAt)})
		return
	}
	w.Header().Set("Location", link.URL)
	w.WriteHeader(http.StatusFound)
}

// sendExportPackage writes a package to the response: for a realm whose storage is not reachable
// by its clients, and for the administrator, who always gets it through the API.
func sendExportPackage(w http.ResponseWriter, r *http.Request, obj *storage.Objects, key, name string) {
	body, info, err := obj.Get(r.Context(), key, "")
	if errors.Is(err, storage.ErrObjectNotFound) {
		writeError(w, r, http.StatusGone, "gone", "the package is no longer in the storage; ask for a new export")
		return
	}
	if err != nil {
		linkError(w, r, err)
		return
	}
	defer body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if info.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 256<<10)
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
		k, rerr := body.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return
			}
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// exportJSON is an export as the API shows it. The administrator's view adds who asked and why.
func exportJSON(e auth.UserExport, admin bool) map[string]any {
	out := map[string]any{"id": e.ID, "status": e.Status, "created_at": formatTime(e.CreatedAt)}
	if !e.FinishedAt.IsZero() {
		out["finished_at"] = formatTime(e.FinishedAt)
	}
	if e.Status == auth.ExportReady {
		counts := e.Counts
		if counts == nil {
			counts = map[string]int64{}
		}
		out["expires_at"], out["size"], out["counts"] = formatTime(e.ExpiresAt), e.Size, counts
	}
	if admin {
		out["user"], out["downloads"] = e.UserID, e.Downloads
		if e.ByAdmin() {
			out["requested_by"] = e.Admin
		} else {
			out["requested_by"] = "user"
		}
		if e.Ticket != "" {
			out["ticket"] = e.Ticket
		}
		if e.Silent {
			out["silent"], out["reason"] = true, e.Reason
		}
		if e.Error != "" {
			out["error"] = e.Error
		}
	}
	return out
}
