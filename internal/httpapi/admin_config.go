package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

// config answers GET /_admin/config: what this instance runs for the realm,
// read from the files it loaded — realm settings, collections, functions and
// the email and page templates, each with the file it came from, plus the
// warnings an operator should know about. It never changes anything, and holds
// no secret: functions name their secrets, never their values.
func (a *adminAPI) config(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "realm")
	rl, ok := a.reg.Realms[name]
	if !ok {
		notFound(w, r)
		return
	}
	access := adminAccessOf(r)
	seesUsers := access.CanRead(rl.Settings, registry.RightUsers)
	rel := func(p string) string {
		if p == "" {
			return ""
		}
		if r, err := filepath.Rel(a.reg.Root, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}

	databases := map[string]any{}
	for _, dbName := range slices.Sorted(mapKeys(rl.Databases)) {
		db := rl.Databases[dbName]
		var cols []any
		for _, c := range db.SortedCollections() {
			cols = append(cols, collectionConfig(c, rel))
		}
		var fns []any
		if db.Functions != nil {
			for _, fnName := range slices.Sorted(mapKeys(db.Functions.Functions)) {
				fns = append(fns, functionConfig(db.Functions.Functions[fnName], rel))
			}
		}
		databases[dbName] = map[string]any{"collections": orEmpty(cols), "functions": orEmpty(fns)}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"realm":       name,
		"fingerprint": a.fingerprint,
		"file":        rel(filepath.Join(a.reg.Root, name, registry.RealmFile)),
		"settings":    realmConfig(rl.Settings, seesUsers),
		"databases":   databases,
		"templates":   templateFiles(filepath.Join(a.reg.Root, name), rel),
		"warnings":    configWarnings(rl.Settings),
	})
}

func orEmpty(in []any) []any {
	if in == nil {
		return []any{}
	}
	return in
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func dur(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// realmConfig is realm.yaml as loaded, defaults applied. Seeded user emails
// are personal data: they show only to whoever may read users.
func realmConfig(s registry.RealmSettings, seesUsers bool) map[string]any {
	roles := map[string]any{}
	for _, name := range slices.Sorted(mapKeys(s.Roles)) {
		role := s.Roles[name]
		access := registry.AdminAccess{Write: role.Admin, ReadAll: role.AdminRead}
		entry := map[string]any{"description": role.Description, "admin": access.String(), "seeded_users": len(role.Users)}
		if seesUsers {
			entry["seeded_emails"] = role.Users
		}
		roles[name] = entry
	}
	t := s.Throttle()
	out := map[string]any{
		"auth":   map[bool]string{true: "enabled", false: "disabled"}[s.AuthEnabled],
		"signup": s.Signup,
		"sessions": map[string]any{
			"idle_timeout": dur(s.IdleTimeout), "max_lifetime": dur(s.MaxLifetime),
			"admin_idle_timeout": dur(s.AdminIdleTimeout), "admin_max_lifetime": dur(s.AdminMaxLifetime),
			"cookie": map[string]any{"enabled": s.Cookie.Enabled, "same_site": s.Cookie.SameSite},
		},
		"password":       map[string]any{"min_length": s.PasswordMinLength},
		"login_throttle": map[string]any{"account_threshold": t.AccountThreshold, "ip_threshold": t.IPThreshold, "window": dur(t.Window), "max_delay": dur(t.MaxDelay)},
		"cors":           map[string]any{"origins": orStrings(s.CORSOrigins)},
		"admin": map[string]any{
			"allowed_networks": orStrings(s.AdminNetworks.Strings()),
			"read_access":      map[string]any{"users": s.ReadAccess.Users, "data": s.ReadAccess.Data},
		},
		"roles":     roles,
		"audit":     map[string]any{"retention": dur(s.AuditRetention)},
		"functions": map[string]any{"max_concurrency": s.FunctionsMaxConcurrency, "log_retention": dur(s.FunctionsLogRetention), "job_retention": dur(s.FunctionsJobRetention)},
		"account": map[string]any{
			"require_verified_email": s.Account.RequireVerifiedEmail, "welcome_email": s.Account.WelcomeEmail,
			"allow_email_change": s.Account.AllowEmailChange, "purge_unverified_after": dur(s.Account.PurgeUnverifiedAfter),
		},
	}
	if e := s.Email; e != nil {
		out["email"] = map[string]any{
			"function": e.Function, "from": e.From, "reply_to": e.ReplyTo, "public_url": e.PublicURL,
			"default_locale": e.DefaultLocale, "locales": orStrings(e.Locales),
			"redirects": e.Redirects, "allowed_redirects": orStrings(e.AllowedRedirects), "links": e.Links,
		}
	}
	return out
}

func orStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func collectionConfig(c *registry.Collection, rel func(string) string) map[string]any {
	indexes := []any{}
	for _, ix := range c.Indexes {
		indexes = append(indexes, map[string]any{"fields": ix.String(), "unique": ix.Unique, "ttl": ix.TTL})
	}
	out := map[string]any{
		"name": c.Name, "schema": c.RawSchema, "schema_file": rel(c.SchemaPath),
		"indexes": indexes, "indexes_file": rel(c.IndexPath),
	}
	if c.Rules != nil {
		ruleSet := map[string]any{}
		for _, op := range []string{"read", "create", "update", "delete", "restore", "purge"} {
			if r := c.Rules.For(rules.Op(op)); r != nil {
				ruleSet[op] = map[string]any{"expression": r.Source, "from": r.Key}
			}
		}
		out["rules"] = ruleSet
		out["rules_file"] = rel(c.Rules.File)
	}
	policy := map[string]any{}
	if c.SoftDelete != nil {
		policy["soft_delete"] = map[string]any{"retention": dur(c.SoftDelete.Retention)}
	}
	if e := c.Erasure; e != nil {
		policy["on_owner_delete"] = map[string]any{"action": e.Action, "remove": orStrings(e.Remove), "replace": e.Replace, "pull": e.Pull, "unset": e.Unset}
	}
	if len(policy) > 0 {
		out["policy"] = policy
		out["policy_file"] = rel(c.ErasurePath)
		if c.ErasurePath == "" {
			out["policy_file"] = rel(filepath.Join(filepath.Dir(c.SchemaPath), registry.CollectionFile))
		}
	}
	return out
}

func functionConfig(fn *registry.Function, rel func(string) string) map[string]any {
	secrets := []string{}
	for _, s := range fn.Secrets {
		secrets = append(secrets, s.String())
	}
	out := map[string]any{
		"name": fn.Name, "file": rel(filepath.Join(fn.Dir, "function.yaml")), "entry": fn.Entry,
		"mode": fn.Mode, "internal": fn.Internal, "admin": fn.Admin, "email": fn.Email, "dev_only": fn.DevOnly,
		"timeout": dur(fn.Timeout), "memory": fn.Memory, "max_output": fn.MaxOutput, "concurrency": fn.Concurrency,
		"idempotency": fn.Idempotency, "schedule": fn.ScheduleExpr, "overlap": fn.Overlap,
		"calls": orStrings(fn.Calls), "secrets": secrets, "network": orStrings(fn.Network),
	}
	if fn.Invoke != nil {
		out["invoke"] = fn.Invoke.Source
	}
	if fn.RateLimit != nil {
		out["rate_limit"] = map[string]any{"per": fn.RateLimit.Per, "limit": fn.RateLimit.Limit, "window": dur(fn.RateLimit.Window)}
	}
	if fn.Retry != nil {
		out["retry"] = map[string]any{"attempts": fn.Retry.Attempts, "backoff": dur(fn.Retry.Backoff), "max_backoff": dur(fn.Retry.MaxBackoff)}
	}
	return out
}

// templateFiles lists the realm's email templates and hosted pages by kind and
// file, as they are on disk.
func templateFiles(realmDir string, rel func(string) string) map[string]any {
	out := map[string]any{}
	for _, dir := range []string{"email", "pages"} {
		kinds := map[string][]string{}
		entries, _ := os.ReadDir(filepath.Join(realmDir, dir))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			files, _ := os.ReadDir(filepath.Join(realmDir, dir, e.Name()))
			for _, f := range files {
				if !f.IsDir() {
					kinds[e.Name()] = append(kinds[e.Name()], rel(filepath.Join(realmDir, dir, e.Name(), f.Name())))
				}
			}
			sort.Strings(kinds[e.Name()])
		}
		out[dir] = kinds
	}
	return out
}

// configWarnings are things in the configuration an operator should know about.
func configWarnings(s registry.RealmSettings) []map[string]any {
	var out []map[string]any
	warn := func(code, message string) { out = append(out, map[string]any{"code": code, "message": message}) }
	switch {
	case !s.AuthEnabled:
		warn("auth_disabled", "authentication is disabled: anyone who can reach this realm can read and write its data")
	case len(s.AdminRoles()) == 0:
		warn("no_admin_role", "no role has an admin level: only existing admin API keys can manage the realm, and `backd bootstrap` can't create an administrator")
	case len(s.FullAdminRoles()) == 0:
		warn("no_full_admin_role", "no role has `admin: true`: `backd bootstrap` can't create the first administrator, and nobody can manage every area")
	}
	if s.AuthEnabled && len(s.AdminNetworks.Strings()) == 0 {
		warn("admin_networks_open", "the admin API is reachable from any network: set admin.allowed_networks")
	}
	if s.AuthEnabled && s.Signup == registry.SignupOpen {
		warn("signup_open", "anyone can create an account (signup: open)")
	}
	if s.Email != nil && strings.Contains(s.Email.Function, "email-capture") {
		warn("email_capture", "the delivery function is email-capture: messages are stored, not sent (development only)")
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}
