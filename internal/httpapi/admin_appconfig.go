package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// appConfigService returns the realm's configuration service, or answers 404 when the
// realm declares none.
func (a *adminAPI) appConfigService(w http.ResponseWriter, r *http.Request) (*appconfig.Service, *registry.AppConfig, bool) {
	rl, ok := a.reg.Realms[chi.URLParam(r, "realm")]
	if !ok {
		notFound(w, r)
		return nil, nil, false
	}
	if rl.AppConfig == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm declares no app configuration (_realm/config/schema.json)")
		return nil, nil, false
	}
	return &appconfig.Service{Store: usersOf(r).Store, Config: rl.AppConfig, Now: usersOf(r).Clock}, rl.AppConfig, true
}

func problemDetails(ps []appconfig.Problem) []Detail {
	out := make([]Detail, len(ps))
	for i, p := range ps {
		out[i] = Detail{Path: p.Path, Reason: p.Reason}
	}
	return out
}

func changesJSON(cs []appconfig.Change) []map[string]any {
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = map[string]any{"path": c.Path, "old": c.Old, "new": c.New}
	}
	return out
}

func entriesJSON(es []appconfig.Entry) []map[string]any {
	out := make([]map[string]any, len(es))
	for i, e := range es {
		item := map[string]any{
			"version": appconfig.FormatVersion(e.Version), "at": formatTime(e.At), "actor": e.Actor,
			"kind": e.Kind, "changes": changesJSON(e.Changes),
		}
		if e.Kind == appconfig.KindRollback {
			item["rolled_back_to"] = appconfig.FormatVersion(e.RolledBackTo)
		}
		out[i] = item
	}
	return out
}

// appConfigEtag is the strong entity tag of a configuration version, which If-Match checks.
func appConfigEtag(v int64) string { return `"` + appconfig.FormatVersion(v) + `"` }

// configBase reads If-Match as the version a change is based on. It answers 428 when the
// header is missing and 400 when it isn't a configuration version.
func configBase(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		writeError(w, r, http.StatusPreconditionRequired, codePreconditionReq, "If-Match is required: send the version you read, such as \"c3\"")
		return 0, false
	}
	v, ok := appconfig.ParseVersion(strings.Trim(raw, `"`))
	if !ok || strings.HasPrefix(raw, "W/") || raw == "*" {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, `If-Match must be a configuration version in quotes, such as "c3"`)
		return 0, false
	}
	return v, true
}

// getAppConfig answers GET /_admin/app-config: every key with its value, the keys
// clients can't read included, the values that no longer validate and the latest history.
func (a *adminAPI) getAppConfig(w http.ResponseWriter, r *http.Request) {
	svc, cfg, ok := a.appConfigService(w, r)
	if !ok {
		return
	}
	view, err := svc.Current(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	hist, err := svc.History(r.Context(), 20)
	if err != nil {
		adminError(w, r, err)
		return
	}
	keys := []map[string]any{}
	for _, p := range cfg.SortedPaths() {
		rs, readable := cfg.Effective(p)
		public := readable
		for _, rule := range rs {
			public = public && rule.AlwaysTrue()
		}
		k := map[string]any{"path": p, "leaf": cfg.Paths[p].Leaf, "readable": readable, "public": public}
		if d := cfg.Paths[p].Description; d != "" {
			k["description"] = d
		}
		keys = append(keys, k)
	}
	body := map[string]any{
		"version": appconfig.FormatVersion(view.Version), "values": view.Values, "overrides": view.Overrides,
		"invalid": orEmptyProblems(view.Invalid), "keys": keys, "history": entriesJSON(hist),
	}
	if view.Version > 0 {
		body["updated_at"], body["updated_by"] = formatTime(view.UpdatedAt), view.UpdatedBy
	}
	w.Header().Set("ETag", appConfigEtag(view.Version))
	writeJSON(w, http.StatusOK, body)
}

func orEmptyProblems(ps []appconfig.Problem) []map[string]string {
	out := make([]map[string]string, len(ps))
	for i, p := range ps {
		out[i] = map[string]string{"path": p.Path, "reason": p.Reason}
	}
	return out
}

// appConfigHistory answers GET /_admin/app-config/history?limit=.
func (a *adminAPI) appConfigHistory(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := a.appConfigService(w, r)
	if !ok {
		return
	}
	limit := appconfig.HistoryVersions
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "limit must be a positive number")
			return
		}
		limit = min(n, limit)
	}
	hist, err := svc.History(r.Context(), limit)
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": entriesJSON(hist)})
}

// configResult answers a change: the new version and what changed, audited by paths only.
func configResult(w http.ResponseWriter, r *http.Request, res appconfig.Result, kind string) {
	if res.Changed {
		paths := make([]string, len(res.Changes))
		for i, c := range res.Changes {
			paths[i] = c.Path
		}
		usersOf(r).Audit(r.Context(), auth.AuditAppConfig, "app-config", map[string]any{
			"version": appconfig.FormatVersion(res.Version), "kind": kind, "paths": paths,
		})
	}
	w.Header().Set("ETag", appConfigEtag(res.Version))
	writeJSON(w, http.StatusOK, map[string]any{
		"version": appconfig.FormatVersion(res.Version), "changed": res.Changed, "changes": changesJSON(res.Changes),
	})
}

// configError answers what a change can fail with.
func configError(w http.ResponseWriter, r *http.Request, err error) {
	var inv *appconfig.InvalidError
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, codeValidation, "the change doesn't satisfy the configuration schema; nothing was applied", problemDetails(inv.Problems)...)
	case errors.Is(err, appconfig.ErrConflict):
		writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "the configuration changed since you read it; read it again and retry")
	case errors.Is(err, appconfig.ErrNoVersion):
		writeError(w, r, http.StatusNotFound, codeNotFound, "the history has no such version (only the last 100 versions, or 90 days, are kept)")
	default:
		adminError(w, r, err)
	}
}

// patchAppConfig answers PATCH /_admin/app-config {set, reset} with If-Match.
func (a *adminAPI) patchAppConfig(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := a.appConfigService(w, r)
	if !ok {
		return
	}
	base, ok := configBase(w, r)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	var details []Detail
	for k := range obj {
		if k != "set" && k != "reset" {
			details = append(details, Detail{Path: k, Reason: "is not allowed: send `set` and `reset`"})
		}
	}
	in := appconfig.Input{}
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
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				details = append(details, Detail{Path: fmt.Sprintf("reset.%d", i), Reason: "must be a string"})
				continue
			}
			in.Reset = append(in.Reset, s)
		}
	}
	if len(in.Set) == 0 && len(in.Reset) == 0 && len(details) == 0 {
		details = append(details, Detail{Path: "", Reason: "send `set`, `reset` or both"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return
	}
	res, err := svc.Apply(r.Context(), base, in, auth.ActorOf(r.Context()))
	if err != nil {
		configError(w, r, err)
		return
	}
	configResult(w, r, res, appconfig.KindSet)
}

// rollbackAppConfig answers POST /_admin/app-config/rollback {version} with If-Match.
func (a *adminAPI) rollbackAppConfig(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := a.appConfigService(w, r)
	if !ok {
		return
	}
	base, ok := configBase(w, r)
	if !ok {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	var to int64
	switch v := obj["version"].(type) {
	case string:
		to, ok = appconfig.ParseVersion(v)
	case json.Number:
		var err error
		to, err = v.Int64()
		ok = err == nil && to >= 0
	default:
		ok = false
	}
	if !ok || len(obj) != 1 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "version", Reason: `must be a configuration version such as "c3" (0 is the defaults)`})
		return
	}
	res, err := svc.Rollback(r.Context(), base, to, auth.ActorOf(r.Context()))
	if err != nil {
		configError(w, r, err)
		return
	}
	configResult(w, r, res, appconfig.KindRollback)
}
