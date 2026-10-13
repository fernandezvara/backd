package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
)

// functionConfig returns the configuration service for a function's callback, or answers.
func (f *functions) functionConfig(w http.ResponseWriter, r *http.Request, write bool) (*appconfig.Service, string, bool) {
	realm := chi.URLParam(r, "realm")
	caller, _, ok := f.docs.identify(w, r, realm)
	if !ok {
		return nil, "", false
	}
	fc := caller.Func
	if fc == nil || (write && !fc.Config) {
		writeError(w, r, http.StatusForbidden, codeForbidden, "this function didn't declare `config: write` in its function.yaml")
		return nil, "", false
	}
	rl, svc := f.docs.reg.Realms[realm], f.docs.users(realm)
	if rl == nil || rl.AppConfig == nil || svc == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm declares no app configuration")
		return nil, "", false
	}
	actor := "function:" + strings.TrimPrefix(fc.Name, realm+"/")
	return &appconfig.Service{Store: svc.Store, Config: rl.AppConfig, Now: f.docs.now}, actor, true
}

// configRead answers GET /v1/{realm}/_fn/config on the internal listener: ctx.config.
// Functions read every value, whatever the clients' read rules say.
func (f *functions) configRead(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := f.functionConfig(w, r, false)
	if !ok {
		return
	}
	view, err := svc.Current(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": view.Values, "version": appconfig.FormatVersion(view.Version)})
}

// configWrite answers PATCH /v1/{realm}/_fn/config {set, reset}: ctx.admin.config.set and
// reset. The change is based on the version just read, retried when another change
// lands in between; it is recorded with the function as actor.
func (f *functions) configWrite(w http.ResponseWriter, r *http.Request) {
	svc, actor, ok := f.functionConfig(w, r, true)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	in := appconfig.Input{}
	var details []Detail
	for k := range obj {
		if k != "set" && k != "reset" {
			details = append(details, Detail{Path: k, Reason: "is not allowed: send `set` and `reset`"})
		}
	}
	if v, has := obj["set"]; has {
		m, ok := v.(map[string]any)
		if !ok {
			details = append(details, Detail{Path: "set", Reason: "must be an object of key path to value"})
		}
		in.Set = m
	}
	if v, has := obj["reset"]; has {
		list, ok := v.([]any)
		if !ok {
			details = append(details, Detail{Path: "reset", Reason: "must be an array of key paths"})
		}
		for _, e := range list {
			if s, ok := e.(string); ok {
				in.Reset = append(in.Reset, s)
			} else {
				details = append(details, Detail{Path: "reset", Reason: "must hold strings"})
			}
		}
	}
	if len(in.Set) == 0 && len(in.Reset) == 0 && len(details) == 0 {
		details = append(details, Detail{Path: "", Reason: "send `set`, `reset` or both"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return
	}
	var res appconfig.Result
	for attempt := 0; attempt < 3; attempt++ {
		cur, err := svc.Current(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
			return
		}
		res, err = svc.Apply(r.Context(), cur.Version, in, actor)
		if errors.Is(err, appconfig.ErrConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			configError(w, r, err)
			return
		}
		break
	}
	if res.Changed {
		paths := make([]string, len(res.Changes))
		for i, c := range res.Changes {
			paths[i] = c.Path
		}
		f.docs.users(chi.URLParam(r, "realm")).AuditAs(r.Context(), actor, auth.AuditAppConfig, "app-config", map[string]any{
			"version": appconfig.FormatVersion(res.Version), "kind": appconfig.KindSet, "paths": paths,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": appconfig.FormatVersion(res.Version), "changed": res.Changed, "changes": changesJSON(res.Changes),
	})
}

// configHistory answers GET /v1/{realm}/_fn/config/history?limit=: ctx.admin.config.history.
func (f *functions) configHistory(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := f.functionConfig(w, r, true)
	if !ok {
		return
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "limit must be a positive number")
			return
		}
		limit = n
	}
	hist, err := svc.History(r.Context(), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": entriesJSON(hist)})
}
