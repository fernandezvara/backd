package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
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
		for p := range strings.SplitSeq(q, ",") {
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

	flat, version, public, signedIn, err := d.readableConfig(r.Context(), realm, caller, hasAuth, inScope)
	if err != nil {
		logger(r.Context()).Error("app configuration unreadable", "realm", realm, "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	values := map[string]any{}
	for _, p := range cfg.SortedPaths() {
		if v, ok := flat[p]; ok {
			docutil.SetPath(values, p, v)
		}
	}

	body, _ := json.Marshal(values)
	sum := sha256.Sum256(body)
	tag := `W/"` + hex.EncodeToString(sum[:])[:32] + `"`
	if len(values) > 0 && public && r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" && !signedIn {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(rl.Settings.AppConfigTTL().Seconds())))
	}
	w.Header().Set("ETag", tag)
	if notModified(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"values": values, "version": appconfig.FormatVersion(version),
		"cache_ttl": int(rl.Settings.AppConfigTTL().Seconds()),
	})
}

// readableConfig is what the caller may read of the realm's configuration, as the values of
// the leaf paths that pass inScope, defaults filled in, with the configuration's version.
// public says every value returned is allowed by a rule that is literally true; signedIn,
// that the caller is a user.
func (d *documents) readableConfig(ctx context.Context, realm string, caller auth.Caller, hasAuth bool, inScope func(string) bool) (flat map[string]any, version int64, public, signedIn bool, err error) {
	rl := d.reg.Realms[realm]
	cfg, svc := rl.AppConfig, d.users(realm)
	view, err := (&appconfig.Service{Store: svc.Store, Config: cfg, Now: d.now}).Current(ctx)
	if err != nil {
		return nil, 0, false, false, err
	}
	vals := rules.Values{Now: d.now().UTC()}
	if hasAuth && caller.User != nil {
		u := caller.User.User
		mfa, methods := caller.SignInMethods()
		vals.User = &rules.User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: u.Roles, MFA: mfa, Methods: methods, Consents: u.Consents}
	}
	flat, public = map[string]any{}, true
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
		flat[p] = docutil.Get(view.Values, p)
	}
	return flat, view.Version, public, vals.User != nil, nil
}
