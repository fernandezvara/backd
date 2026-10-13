package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/rules"
)

// readAppConfig answers GET /v1/{realm}/_config[?paths=a,b]: the app configuration
// the caller may read, defaults filled in (design/app-config.md §5). What the rules
// don't allow is absent, with no error and no hint.
func (d *documents) readAppConfig(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	rl := d.reg.Realms[realm]
	svc := d.users(realm)
	if rl == nil || rl.AppConfig == nil || svc == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm declares no app configuration")
		return
	}
	cfg := rl.AppConfig
	caller, hasAuth, ok := d.identify(w, r, realm)
	if !ok {
		return
	}

	// Which paths: all, or the branches asked for.
	var roots []string
	if q := r.URL.Query().Get("paths"); q != "" {
		for _, p := range strings.Split(q, ",") {
			p = strings.TrimSpace(p)
			if !cfg.HasPath(p) {
				writeError(w, r, http.StatusBadRequest, codeInvalidPath, "paths must name declared configuration keys or groups",
					Detail{Path: "paths", Reason: strconv.Quote(p) + " is not a configuration key"})
				return
			}
			roots = append(roots, p)
		}
	}
	inScope := func(leaf string) bool {
		if len(roots) == 0 {
			return true
		}
		for _, p := range roots {
			if leaf == p || strings.HasPrefix(leaf, p+".") {
				return true
			}
		}
		return false
	}

	view, err := (&appconfig.Service{Store: svc.Store, Config: cfg, Now: d.now}).Current(r.Context())
	if err != nil {
		logger(r.Context()).Error("app configuration unreadable", "realm", realm, "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}

	vals := rules.Values{Now: d.now().UTC()}
	if hasAuth && caller.User != nil {
		u := caller.User.User
		mfa, methods := caller.SignInMethods()
		vals.User = &rules.User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: u.Roles, MFA: mfa, Methods: methods, Consents: u.Consents}
	}

	values := map[string]any{}
	public := true
	for _, p := range cfg.SortedPaths() {
		if !cfg.Paths[p].Leaf || !inScope(p) {
			continue
		}
		chain, ruled := cfg.Effective(p)
		if !ruled {
			continue
		}
		allowed, literal := true, true
		for _, rule := range chain {
			ok, err := rule.Allow(vals)
			if err != nil || !ok {
				allowed = false
				break
			}
			literal = literal && rule.AlwaysTrue()
		}
		if !allowed {
			continue
		}
		public = public && literal
		setNested(values, p, valueAt(view.Values, p))
	}

	body, _ := json.Marshal(values)
	sum := sha256.Sum256(body)
	tag := `W/"` + hex.EncodeToString(sum[:])[:32] + `"`
	if len(values) > 0 && public && r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" && vals.User == nil {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(rl.Settings.AppConfigTTL().Seconds())))
	}
	w.Header().Set("ETag", tag)
	if notModified(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"values": values, "version": appconfig.FormatVersion(view.Version),
		"cache_ttl": int(rl.Settings.AppConfigTTL().Seconds()),
	})
}

// valueAt returns the value at a dot path of a nested document.
func valueAt(m map[string]any, path string) any {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		next, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = next[k]
	}
	return cur
}

// setNested puts v at a dot path of a nested document.
func setNested(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}
