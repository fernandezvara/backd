package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
)

// The administrator's side of a user's personal data export (design/compliance.md §4.1): support
// produces the same package for a formal request. The package goes to the administrator, who
// downloads it through the API; the user is told, unless the administrator says not to and gives
// the reason, which is kept in the audit record.

const (
	maxExportTicket = 200
	maxExportReason = 500
)

// exportUser handles POST /users/{id}/export {ticket?, no_notify?, reason?}: it needs full admin
// access and a fresh re-authentication, and queues the export of the user.
func (a *adminAPI) exportUser(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "exporting a user's personal data") {
		return
	}
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	if rl == nil || !rl.Settings.Compliance.Export {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm does not export its users' data (compliance.export in realm.yaml)")
		return
	}
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"ticket": "string?", "no_notify": "bool?", "reason": "string?"})
	if !ok {
		return
	}
	ticket, _ := f["ticket"].(string)
	reason, _ := f["reason"].(string)
	silent, _ := f["no_notify"].(bool)
	ticket, reason = strings.TrimSpace(ticket), strings.TrimSpace(reason)
	switch {
	case utf8.RuneCountInString(ticket) > maxExportTicket:
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "ticket", Reason: "is too long"})
		return
	case utf8.RuneCountInString(reason) > maxExportReason:
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "is too long"})
		return
	case silent && reason == "":
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "is required with no_notify: say why the user is not told, it is kept in the audit record"})
		return
	case !silent && reason != "":
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "only applies with no_notify"})
		return
	}
	svc, u := usersOf(r), adminUserOf(r)
	if !u.ErasedAt.IsZero() {
		writeError(w, r, http.StatusConflict, codeConflict, "this user was erased: there is nothing left to export")
		return
	}
	caller, _ := callerOf(r)
	rec, err := svc.RequestUserExport(r.Context(), u.ID, auth.ExportRequest{
		Admin: caller.Actor(), Ticket: ticket, Notify: !silent, Reason: reason, RequestID: requestID(r.Context()),
	})
	switch {
	case errors.Is(err, auth.ErrExportRunning):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	details := map[string]any{"export": rec.ID}
	if ticket != "" {
		details["ticket"] = ticket
	}
	action := auth.AuditDataExportRequested
	if silent {
		// Listed apart from the exports the user was told about.
		action, details["reason"] = auth.AuditDataExportSilent, reason
	}
	svc.Audit(r.Context(), action, "user:"+u.ID, details)

	out := exportJSON(rec, true)
	if !silent {
		_, err := svc.QueueEmail(r.Context(), auth.EmailRequest{Kind: email.DataExportMade, UserID: u.ID, Locale: rec.Locale, Notice: true, ExportID: rec.ID, RequestID: requestID(r.Context())})
		out["notified"] = err == nil
		if err != nil {
			// The export goes on; the administrator is told the user was not.
			logger(r.Context()).Warn("could not queue the email that tells a user an export was made", "export", rec.ID, "error", err)
			out["notify_error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusAccepted, out)
}

// userExports handles GET /users/{id}/exports: all the exports of the user, those they asked for and
// those made for them, with who asked and whether they were told.
func (a *adminAPI) userExports(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).UserExports(r.Context(), adminUserOf(r).ID)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, e := range list {
		items[i] = exportJSON(e, true)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *adminAPI) exportOf(w http.ResponseWriter, r *http.Request) (auth.UserExport, bool) {
	rec, err := usersOf(r).UserExport(r.Context(), adminUserOf(r).ID, chi.URLParam(r, "export"))
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such export")
		return auth.UserExport{}, false
	}
	if err != nil {
		adminError(w, r, err)
		return auth.UserExport{}, false
	}
	return rec, true
}

// userExport handles GET /users/{id}/exports/{export}, for the CLI that waits for it.
func (a *adminAPI) userExport(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.exportOf(w, r)
	if !ok {
		return
	}
	out := exportJSON(rec, true)
	if rec.Status == auth.ExportQueued || rec.Status == auth.ExportRunning {
		if job, found, err := usersOf(r).GetJob(r.Context(), rec.JobID); err == nil && found && len(job.Steps) > 0 {
			out["step"] = job.Steps[len(job.Steps)-1].Name
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// downloadUserExport handles GET /users/{id}/exports/{export}/download: the package an administrator
// asked for, through the API (never a link to the storage). It needs full admin access and a fresh
// re-authentication, as making it did, and is audited. An export the user made for themselves is
// theirs: an administrator asks for their own.
func (a *adminAPI) downloadUserExport(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "downloading a user's personal data") {
		return
	}
	rec, ok := a.exportOf(w, r)
	if !ok {
		return
	}
	svc := usersOf(r)
	switch {
	case !rec.ByAdmin():
		writeError(w, r, http.StatusForbidden, codeForbidden, "the user asked for this export for themselves: it is theirs to download. Make one with POST /users/{id}/export")
		return
	case rec.Status != auth.ExportReady || rec.Key == "":
		writeError(w, r, http.StatusConflict, codeConflict, "this export is "+rec.Status+": there is nothing to download")
		return
	case !rec.ExpiresAt.After(svc.Clock()):
		writeError(w, r, http.StatusGone, "gone", "this export has expired; make a new one")
		return
	}
	obj, err := a.fns.docs.objects.For(r.Context(), chi.URLParam(r, "realm"), false)
	if err != nil {
		linkError(w, r, err)
		return
	}
	caller, _ := callerOf(r)
	rec.Downloads++
	if err := svc.Store.ReplaceUserExport(r.Context(), rec); err != nil {
		logger(r.Context()).Error("count an export download", "export", rec.ID, "error", err)
	}
	svc.Audit(r.Context(), auth.AuditDataExportDownloaded, "user:"+rec.UserID, map[string]any{"export": rec.ID, "by": caller.Actor()})
	sendExportPackage(w, r, obj, rec.Key, chi.URLParam(r, "realm")+"-data-export-"+rec.CreatedAt.UTC().Format("2006-01-02")+".zip")
}
