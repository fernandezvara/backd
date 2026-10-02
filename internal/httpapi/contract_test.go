package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fernandezvara/backd/internal/executor"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

// contract checks HTTP responses against api/openapi.yaml.
type contract struct {
	spec     map[string]any
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	schemas  map[string]*jsonschema.Schema
	seen     map[string]bool // "METHOD path status" produced by the tests
	failures []string
}

const specURL = "file:///openapi.json"

// errConflictForContract makes the test store fail writes with a unique
// index violation.
var errConflictForContract = &storage.ConflictError{Fields: []string{"title"}}

func loadContract(t *testing.T) *contract {
	t.Helper()
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var y any
	if err := yaml.Unmarshal(data, &y); err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON so the schema compiler sees JSON types.
	j, err := json.Marshal(y)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(specURL, doc); err != nil {
		t.Fatal(err)
	}
	return &contract{spec: doc.(map[string]any), compiler: c, schemas: map[string]*jsonschema.Schema{}, seen: map[string]bool{}}
}

// pointer builds a JSON pointer URL into the spec.
func pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		t = strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1")
		b.WriteByte('/')
		b.WriteString(url.PathEscape(t))
	}
	return specURL + "#" + b.String()
}

func (c *contract) schema(ptr string) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.schemas[ptr]; ok {
		return s, nil
	}
	s, err := c.compiler.Compile(ptr)
	if err != nil {
		return nil, err
	}
	c.schemas[ptr] = s
	return s, nil
}

// deref follows a local $ref, returning the target and its token path.
func (c *contract) deref(v map[string]any, tokens []string) (map[string]any, []string) {
	for {
		ref, ok := v["$ref"].(string)
		if !ok {
			return v, tokens
		}
		tokens = strings.Split(strings.TrimPrefix(ref, "#/"), "/")
		var cur any = c.spec
		for _, t := range tokens {
			cur = cur.(map[string]any)[t]
		}
		v = cur.(map[string]any)
	}
}

func (c *contract) paths() map[string]any { return c.spec["paths"].(map[string]any) }

// anywhereStatuses are the errors the spec's default response covers,
// because any route can return them: an unknown realm, body limits,
// failures and unavailability.
var anywhereStatuses = map[int]bool{404: true, 413: true, 415: true, 500: true, 503: true}

// check validates one response to method on the route pattern.
func (c *contract) check(method, pattern string, status int, h http.Header, body []byte) error {
	pathItem, ok := c.paths()[pattern].(map[string]any)
	if !ok {
		return fmt.Errorf("route %s is not in the spec", pattern)
	}
	op, ok := pathItem[strings.ToLower(method)].(map[string]any)
	if !ok {
		return fmt.Errorf("%s %s is not in the spec", method, pattern)
	}
	responses := op["responses"].(map[string]any)
	code := fmt.Sprint(status)
	key := code
	resp, ok := responses[code].(map[string]any)
	if !ok {
		// A range such as 4XX (function errors choose their own status).
		if r, found := responses[code[:1]+"XX"].(map[string]any); found {
			resp, ok, code, key = r, true, code[:1]+"XX", code[:1]+"XX"
		}
	}
	if !ok {
		// default only stands for the errors any route can return; every
		// other status must be listed on the operation.
		if resp, ok = responses["default"].(map[string]any); !ok || !anywhereStatuses[status] {
			return fmt.Errorf("status %d is not documented", status)
		}
		key = "default"
	} else {
		c.mu.Lock()
		c.seen[method+" "+pattern+" "+code] = true
		c.mu.Unlock()
	}
	resp, tokens := c.deref(resp, []string{"paths", pattern, strings.ToLower(method), "responses", key})

	var errs []error
	for name, hv := range asMap(resp["headers"]) {
		hdr, htokens := c.deref(hv.(map[string]any), append(slices.Clone(tokens), "headers", name))
		val := h.Get(name)
		if val == "" {
			if req, _ := hdr["required"].(bool); req {
				errs = append(errs, fmt.Errorf("missing required header %s", name))
			}
			continue
		}
		s, err := c.schema(pointer(append(htokens, "schema")...))
		if err != nil {
			return err
		}
		if err := s.Validate(val); err != nil {
			errs = append(errs, fmt.Errorf("header %s = %q: %v", name, val, err))
		}
	}

	content := asMap(resp["content"])
	if len(content) == 0 {
		if len(body) > 0 {
			errs = append(errs, fmt.Errorf("body not documented: %s", body))
		}
		return errors.Join(errs...)
	}
	// Match the actual Content-Type against whichever media type this
	// response documents (usually just application/json, but a webhook
	// function's own response, roadmap F13, is deliberately not JSON,
	// so it's documented as */* — checked only as a fallback, after
	// every specific media type, so it never shadows a real match).
	ct := h.Get("Content-Type")
	var mediaKey string
	for k := range content {
		if k != "*/*" && strings.HasPrefix(ct, k) {
			mediaKey = k
			break
		}
	}
	if mediaKey == "" {
		if _, ok := content["*/*"]; ok {
			mediaKey = "*/*"
		}
	}
	if mediaKey == "" {
		return errors.Join(append(errs, fmt.Errorf("Content-Type = %q, not documented for this response", ct))...)
	}
	if mediaKey != "application/json" {
		// A non-JSON media type is opaque to this checker: documented,
		// but its body isn't schema-validated.
		return errors.Join(errs...)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("body is not JSON: %v", err))...)
	}
	s, err := c.schema(pointer(append(tokens, "content", "application/json", "schema")...))
	if err != nil {
		return err
	}
	if err := s.Validate(inst); err != nil {
		errs = append(errs, fmt.Errorf("body %s: %v", body, err))
	}
	return errors.Join(errs...)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// wrap checks every response of h against the contract.
func (c *contract) wrap(t *testing.T, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		pattern := strings.TrimSuffix(rctx.RoutePattern(), "/")
		if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
			// Middleware answered before the final route matched.
			pattern = c.match(prefix, r.Method, r.URL.Path)
		}
		if pattern == "" || rec.status == http.StatusMethodNotAllowed || r.Method == http.MethodOptions {
			return // unknown routes and CORS preflights aren't operations
		}
		if err := c.check(r.Method, pattern, rec.status, w.Header(), rec.body.Bytes()); err != nil {
			c.mu.Lock()
			c.failures = append(c.failures, fmt.Sprintf("%s %s → %d: %v", r.Method, r.URL.Path, rec.status, err))
			c.mu.Unlock()
		}
	})
}

// match finds the spec path under prefix that matches path for method,
// preferring the one with the most literal segments.
func (c *contract) match(prefix, method, path string) string {
	best, bestLiterals := "", -1
	got := strings.Split(strings.Trim(path, "/"), "/")
	for tmpl, item := range c.paths() {
		if !strings.HasPrefix(tmpl, prefix) {
			continue
		}
		if _, ok := asMap(item)[strings.ToLower(method)]; !ok {
			continue
		}
		want := strings.Split(strings.Trim(tmpl, "/"), "/")
		if len(want) != len(got) {
			continue
		}
		literals, ok := 0, true
		for i := range want {
			switch {
			case strings.HasPrefix(want[i], "{"):
			case want[i] == got[i]:
				literals++
			default:
				ok = false
			}
		}
		if ok && literals > bestLiterals {
			best, bestLiterals = tmpl, literals
		}
	}
	return best
}

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// verify reports contract failures, spec operations without a route,
// routes without a spec operation, and documented statuses no test produced.
func (c *contract) verify(t *testing.T, router chi.Routes) {
	t.Helper()
	for _, f := range c.failures {
		t.Error(f)
	}
	routes := map[string]bool{}
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+strings.TrimSuffix(route, "/")] = true
		return nil
	})
	for route := range routes {
		method, path, _ := strings.Cut(route, " ")
		if _, ok := asMap(c.paths()[path])[strings.ToLower(method)]; !ok {
			t.Errorf("route %s is missing from the spec", route)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(c.paths())) {
		for method, op := range asMap(c.paths()[path]) {
			if method == "parameters" {
				continue
			}
			m := strings.ToUpper(method)
			if !routes[m+" "+path] {
				t.Errorf("spec operation %s %s has no route", m, path)
			}
			for code := range asMap(asMap(op)["responses"]) {
				if code != "default" && !c.seen[m+" "+path+" "+code] {
					t.Errorf("%s %s: documented status %s never produced by the contract tests", m, path, code)
				}
			}
		}
	}
}

// TestContract drives every operation through every documented status and
// checks each response against api/openapi.yaml.
func TestContract(t *testing.T) {
	c := loadContract(t)
	f := newRulesFixture(t)
	router := f.h.(chi.Routes)
	f.h = c.wrap(t, f.h)

	req := func(method, path, body string, hdr map[string]string, want int) map[string]any {
		t.Helper()
		h := map[string]string{}
		for k, v := range hdr {
			h[k] = v
		}
		if body != "" && h["Content-Type"] == "" {
			h["Content-Type"] = "application/json"
			if method == "PATCH" {
				h["Content-Type"] = "application/merge-patch+json"
			}
		}
		rec, out := f.doH(t, method, path, body, h)
		if rec.Code != want {
			t.Errorf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body)
		}
		return out
	}
	key, ada, carl := bearer(f.key), bearer(f.ada), bearer(f.carl)
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for a, b := range h {
			out[a] = b
		}
		return out
	}

	// Health.
	req("GET", "/healthz", "", nil, 200)
	req("GET", "/readyz", "", nil, 200)
	// Readiness reports the config fingerprint.
	fp := newFixture(t, func(cfg *Config) { cfg.ConfigFingerprint = strings.Repeat("0a", 32) })
	fp.h = c.wrap(t, fp.h)
	if rec, out := fp.do(t, "GET", "/readyz", ""); rec.Code != 200 || out["config"] != strings.Repeat("0a", 32) {
		t.Errorf("readyz with a fingerprint: %d %v", rec.Code, out)
	}
	down := newFixture(t, func(cfg *Config) { cfg.Ready = func(context.Context) error { return errors.New("down") } })
	down.h = c.wrap(t, down.h)
	if rec, _ := down.do(t, "GET", "/readyz", ""); rec.Code != 503 {
		t.Errorf("readyz down: %d", rec.Code)
	}

	// Auth.
	a := authBase
	req("POST", a+"/signup", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`, nil, 201)
	req("POST", a+"/signup", `{"email": "nope", "password": "dev-p4ssw0rd!"}`, nil, 400)
	req("POST", a+"/signup", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, nil, 409)
	f.svc.Settings.Signup = "closed"
	req("POST", a+"/signup", `{"email": "x@example.com", "password": "dev-p4ssw0rd!"}`, nil, 403)
	f.svc.Settings.Signup = "open"

	// Email verification.
	f.svc.Now = func() time.Time { return *f.clock }
	verifyToken := func() string { return f.token(t, "verify-email", "") }
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	req("POST", a+"/signup", `{"email": "r@example.com", "password": "dev-p4ssw0rd!", "redirect_to": "https://evil.example/"}`, nil, 400)
	f.svc.Settings.Account.RequireVerifiedEmail = true
	req("POST", a+"/signup", `{"email": "pending@example.com", "password": "dev-p4ssw0rd!", "redirect_to": "https://app.acme.example/welcome"}`, nil, 202)
	req("POST", a+"/login", `{"email": "pending@example.com", "password": "dev-p4ssw0rd!"}`, nil, 403)
	f.svc.Settings.Account.RequireVerifiedEmail = false
	req("POST", a+"/verify-email/resend", `{"email": "pending@example.com"}`, nil, 202)
	req("POST", a+"/verify-email/resend", `{"email": "nobody@example.com", "redirect_to": "https://app.acme.example/x"}`, nil, 202)
	req("POST", a+"/verify-email/resend", `{"email": "nope"}`, nil, 400)
	req("POST", a+"/verify-email/resend", `{"email": "a@example.com"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/verify-email/resend", `{"email": "a@example.com"}`, nil, 404)
	req("GET", a+"/verify-email?token="+verifyToken(), "", nil, 200)
	req("GET", a+"/verify-email?token=nope", "", nil, 400)
	req("GET", "/v1/nope/_auth/verify-email?token=x", "", nil, 404)
	req("POST", a+"/verify-email", "token="+verifyToken(), form, 200)
	req("POST", a+"/verify-email", "token=nope", form, 400)
	req("POST", a+"/verify-email", `{"token": "`+verifyToken()+`"}`, nil, 204)
	req("POST", a+"/verify-email", `{"token": "nope"}`, nil, 400)
	req("POST", a+"/verify-email", `{"token": "x"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/verify-email", `{"token": "x"}`, nil, 404)

	login := req("POST", a+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)
	me := bearer(login["token"].(string))
	req("POST", a+"/login", `{"email": "new@example.com"}`, nil, 400)
	for range 5 {
		req("POST", a+"/login", `{"email": "victim@example.com", "password": "dev-p4ssw0rd!0"}`, nil, 401)
	}
	req("POST", a+"/login", `{"email": "victim@example.com", "password": "dev-p4ssw0rd!0"}`, nil, 429)

	req("GET", a+"/me", "", me, 200)
	// The user's language: a listed one is accepted, any other is refused.
	req("PATCH", a+"/me", `{"locale": "es"}`, with(me, "Content-Type", "application/json"), 200)
	req("PATCH", a+"/me", `{"locale": "fr"}`, with(me, "Content-Type", "application/json"), 400)
	req("PATCH", a+"/me", `{"locale": "es"}`, map[string]string{"Content-Type": "application/json"}, 401)
	sessions := req("GET", a+"/sessions", "", me, 200)
	sid := sessions["items"].([]any)[0].(map[string]any)["id"].(string)
	for _, op := range [][2]string{{"POST", "/logout"}, {"POST", "/logout-all"}, {"GET", "/me"}, {"GET", "/sessions"}, {"DELETE", "/sessions/" + sid}} {
		req(op[0], a+op[1], "", nil, 401)
	}
	req("DELETE", a+"/sessions/nope", "", me, 404)
	req("POST", a+"/password", `{"current_password": "dev-p4ssw0rd!"}`, me, 400)
	req("POST", a+"/password", `{"current_password": "dev-p4ssw0rd!0", "new_password": "dev-p4ssw0rd!2"}`, me, 401)
	req("POST", a+"/password", `{"current_password": "dev-p4ssw0rd!", "new_password": "dev-p4ssw0rd!2"}`, me, 204)
	req("DELETE", a+"/me", `{}`, me, 400)
	req("DELETE", a+"/me", `{"password": "dev-p4ssw0rd!0"}`, me, 401)
	other := req("POST", a+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!2"}`, nil, 200)
	otherTok := bearer(other["token"].(string))
	otherSessions := req("GET", a+"/sessions", "", otherTok, 200)
	for _, it := range otherSessions["items"].([]any) {
		if it.(map[string]any)["current"] == false {
			req("DELETE", a+"/sessions/"+it.(map[string]any)["id"].(string), "", otherTok, 204)
		}
	}
	req("POST", a+"/logout-all", "", otherTok, 204)
	third := req("POST", a+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!2"}`, nil, 200)
	req("POST", a+"/logout", "", bearer(third["token"].(string)), 204)
	fourth := req("POST", a+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!2"}`, nil, 200)
	req("DELETE", a+"/me", `{"password": "dev-p4ssw0rd!2"}`, bearer(fourth["token"].(string)), 204)

	// Admin: every operation refuses callers without a key.
	ad := admin
	adminOps := [][3]string{
		{"GET", "/users", ""}, {"POST", "/users", `{"email": "z@example.com"}`},
		{"GET", "/users/" + f.bobID, ""}, {"PATCH", "/users/" + f.bobID, `{"disabled": false}`},
		{"DELETE", "/users/" + f.bobID, ""}, {"POST", "/users/" + f.bobID + "/password", `{"password": "dev-p4ssw0rd!"}`},
		{"PUT", "/users/" + f.bobID + "/roles/admin", ""}, {"DELETE", "/users/" + f.bobID + "/roles/admin", ""},
		{"GET", "/invitations", ""}, {"POST", "/invitations", `{}`}, {"DELETE", "/invitations/x", ""},
	}
	for _, op := range adminOps {
		h := map[string]string{}
		if op[0] == "PATCH" && op[2] != "" {
			h["Content-Type"] = "application/json"
		}
		req(op[0], ad+op[1], op[2], h, 401)
		req(op[0], ad+op[1], op[2], with(ada, "Content-Type", "application/json"), 403)
	}
	json := func(h map[string]string) map[string]string { return with(h, "Content-Type", "application/json") }
	req("GET", ad+"/users?limit=2", "", key, 200)
	req("GET", ad+"/users?limit=0", "", key, 400)
	created := req("POST", ad+"/users", `{"email": "dan@example.com", "password": "dev-p4ssw0rd!"}`, key, 201)
	dan := created["id"].(string)
	req("POST", ad+"/users", `{"email": "nope"}`, key, 400)
	req("POST", ad+"/users", `{"email": "dan@example.com"}`, key, 409)
	req("GET", ad+"/users/"+dan, "", key, 200)
	req("GET", ad+"/users/nope", "", key, 404)
	req("PATCH", ad+"/users/"+dan, `{"email_verified": true}`, json(key), 200)
	req("PATCH", ad+"/users/"+dan, `{"email": "x@example.com"}`, json(key), 400)
	req("PATCH", ad+"/users/nope", `{"disabled": true}`, json(key), 404)
	req("POST", ad+"/users/"+dan+"/password", `{"password": "dev-p4ssw0rd!2"}`, key, 204)
	req("POST", ad+"/users/"+dan+"/password", `{"password": "short"}`, key, 400)
	req("POST", ad+"/users/nope/password", `{"password": "dev-p4ssw0rd!2"}`, key, 404)
	req("PUT", ad+"/users/"+dan+"/roles/admin", "", key, 200)
	req("PUT", ad+"/users/"+dan+"/roles/root", "", key, 400)
	req("PUT", ad+"/users/nope/roles/admin", "", key, 404)
	req("DELETE", ad+"/users/"+dan+"/roles/admin", "", key, 200)
	req("DELETE", ad+"/users/nope/roles/admin", "", key, 404)
	inv := req("POST", ad+"/invitations", `{"email": "eve@example.com", "expires_in": "3d"}`, key, 201)
	req("POST", ad+"/invitations", `{}`, key, 201)
	req("POST", ad+"/invitations", `{"expires_in": "forever"}`, key, 400)
	req("GET", ad+"/invitations", "", key, 200)
	req("DELETE", ad+"/invitations/"+inv["id"].(string), "", key, 204)
	req("DELETE", ad+"/invitations/nope", "", key, 404)
	nets := ad + "/users/" + dan + "/networks"
	req("PUT", nets, `{"admin_networks": ["10.0.0.0/8"], "login_networks": []}`, key, 200)
	req("PUT", nets, `{"admin_networks": ["office"], "login_networks": []}`, key, 400)
	req("PUT", nets, `{"admin_networks": [], "login_networks": []}`, nil, 401)
	req("PUT", nets, `{"admin_networks": [], "login_networks": []}`, ada, 403)
	req("PUT", ad+"/users/nope/networks", `{"admin_networks": [], "login_networks": []}`, key, 404)
	req("GET", ad+"/apikeys", "", key, 200)
	req("GET", ad+"/apikeys", "", nil, 401)
	req("GET", ad+"/apikeys", "", ada, 403)
	req("POST", ad+"/apikeys", `{"name": "contract", "role": "data", "expires_in": "1d", "networks": ["192.0.2.0/24"]}`, key, 201)
	req("POST", ad+"/apikeys", `{"name": "contract"}`, key, 409)
	req("POST", ad+"/apikeys", `{"name": "x", "role": "root"}`, key, 400)
	req("POST", ad+"/apikeys", `{"name": "x"}`, nil, 401)
	req("POST", ad+"/apikeys", `{"name": "x"}`, ada, 403)
	req("DELETE", ad+"/apikeys/contract", "", key, 204)
	req("DELETE", ad+"/apikeys/contract", "", key, 404)
	// The audit trail holds the key's creation and revocation, with details.
	if out := req("GET", ad+"/audit?target=key:contract&limit=5", "", key, 200); len(out["items"].([]any)) != 2 {
		t.Errorf("audit records for the key: %v", out["items"])
	}
	req("GET", ad+"/audit?since=yesterday", "", key, 400)
	req("GET", ad+"/audit", "", nil, 401)
	req("GET", ad+"/audit", "", ada, 403)

	// Invocation history (roadmap F10): the functions called above were
	// already recorded.
	req("GET", ad+"/invocations?function=app/echo&limit=1", "", key, 200)
	req("GET", ad+"/invocations?since=yesterday", "", key, 400)
	req("GET", ad+"/invocations", "", nil, 401)
	req("GET", ad+"/invocations", "", ada, 403)

	// Secrets (roadmap F5).
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	req("GET", ad+"/secrets", "", key, 200)
	req("GET", ad+"/secrets", "", nil, 401)
	req("GET", ad+"/secrets", "", ada, 403)
	req("PUT", ad+"/secrets/SHARED", `{"value": "shared-value"}`, json(key), 204)
	req("PUT", ad+"/secrets/KEY", `{"value": "sk_live_1", "database": "app"}`, json(key), 204)
	req("PUT", ad+"/secrets/bad-name", `{"value": "x"}`, json(key), 400)
	req("PUT", ad+"/secrets/KEY", `{"value": ""}`, json(key), 400)
	req("PUT", ad+"/secrets/KEY", `{"value": "x", "database": "nope"}`, json(key), 400)
	req("PUT", ad+"/secrets/KEY", `{"value": "x"}`, nil, 401)
	req("PUT", ad+"/secrets/KEY", `{"value": "x"}`, with(ada, "Content-Type", "application/json"), 403)
	if out := req("GET", ad+"/secrets", "", key, 200); len(out["items"].([]any)) != 2 {
		t.Errorf("secrets listed: %v", out["items"])
	}
	req("DELETE", ad+"/secrets/SHARED", "", key, 204)
	req("DELETE", ad+"/secrets/SHARED", "", key, 404)
	req("DELETE", ad+"/secrets/KEY?database=app", "", key, 204)
	req("DELETE", ad+"/secrets/KEY", "", nil, 401)
	req("DELETE", ad+"/secrets/KEY", "", ada, 403)

	// Functions (fake executor).
	fn := "/v1/acme/app/_func/"
	req("POST", fn+"echo", `{"n": 1}`, ada, 200)
	req("POST", fn+"echo", `{"n": 1}`, nil, 401)
	req("POST", fn+"echo", `{"n": 1}`, carl, 403)
	req("POST", fn+"typed", `{"n": "x"}`, ada, 400)
	jobOut := req("POST", fn+"job", `{}`, ada, 202)
	req("GET", "/v1/acme/app/_jobs/"+jobOut["id"].(string), "", ada, 200)
	req("GET", "/v1/acme/app/_jobs/"+jobOut["id"].(string), "", nil, 401)
	req("GET", "/v1/acme/app/_jobs/"+jobOut["id"].(string), "", bearer(f.bob), 404)
	req("GET", "/v1/acme/app/_jobs/nonexistent", "", ada, 404)
	// The admin listing of jobs (roadmap F19).
	req("GET", ad+"/jobs?function=app/job&status=queued&limit=5", "", key, 200)
	req("GET", ad+"/jobs?scheduled=false&since=2020-01-01T00:00:00Z", "", key, 200)
	req("GET", ad+"/jobs?status=paused", "", key, 400)
	req("GET", ad+"/jobs?function=job", "", key, 400)
	req("GET", ad+"/jobs", "", nil, 401)
	req("GET", ad+"/jobs", "", ada, 403)
	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Webhook: &executor.WebhookResponse{Status: 200, Body: "ok"}}, nil
	})
	req("POST", fn+"hook", `{}`, ada, 200)
	f.runner.set(nil)
	req("POST", fn+"nope", `{}`, ada, 404)

	// Idempotency (roadmap F12).
	req("POST", fn+"billed", `{}`, ada, 400) // idempotency: required, no key
	req("POST", fn+"billed", `{}`, with(ada, "Idempotency-Key", "order-1"), 200)
	req("POST", fn+"echo", `{"n": 1}`, with(ada, "Idempotency-Key", "call-1"), 200)
	req("POST", fn+"echo", `{"n": 1}`, with(ada, "Idempotency-Key", "call-1"), 200) // replay
	req("POST", fn+"echo", `{"n": 2}`, with(ada, "Idempotency-Key", "call-1"), 422) // different input
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 409, Code: "out_of_stock", Message: "no stock"}}, nil
	})
	req("POST", fn+"echo", `{}`, ada, 409)
	// Any other 4xx the function chooses (documented as 4XX).
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 418, Code: "teapot", Message: "short and stout"}}, nil
	})
	req("POST", fn+"echo", `{}`, ada, 418)
	// An internal function has no route.
	req("POST", fn+"cleanup", `{}`, ada, 404)
	// Running functions by hand (internal ones included).
	run := ad + "/functions/app/"
	f.runner.set(nil)
	req("POST", run+"cleanup/invoke", `{"input": {"n": 1}}`, key, 202)
	req("POST", run+"echo/invoke", `{"input": {"n": 1}, "as": "ada@example.com"}`, key, 200)
	req("POST", run+"echo/invoke", `{"as": "nobody@example.com"}`, key, 404)
	req("POST", run+"nothing_here/invoke", `{}`, key, 404)
	req("POST", run+"hook/invoke", `{}`, key, 400)
	req("POST", run+"typed/invoke", `{"input": {"n": "x"}}`, key, 400)
	req("POST", run+"echo/invoke", `{}`, nil, 401)
	req("POST", run+"echo/invoke", `{}`, with(ada, "Content-Type", "application/json"), 403)
	if _, _, err := f.svc.Signup(context.Background(), "gone@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	_ = f.svc.SetDisabled(context.Background(), "gone@example.com", true)
	req("POST", run+"echo/invoke", `{"as": "gone@example.com"}`, key, 409)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 418, Code: "teapot", Message: "short and stout"}}, nil
	})
	req("POST", run+"echo/invoke", `{}`, key, 418)
	f.runner.set(nil)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusTimeout}, nil
	})
	req("POST", fn+"echo", `{}`, ada, 504)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusError}, nil
	})
	req("POST", fn+"echo", `{}`, ada, 500)

	// 429: the function is at its per-instance concurrency limit (F8).
	started, release := make(chan struct{}), make(chan struct{})
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		close(started)
		<-release
		return executor.Result{Status: executor.StatusOK, Output: []byte("null")}, nil
	})
	go f.doH(t, "POST", fn+"limited", `{}`, ada)
	<-started
	req("POST", fn+"limited", `{}`, ada, 429)
	close(release)

	f.runner.set(nil)
	req("DELETE", ad+"/apikeys/contract", "", nil, 401)
	req("DELETE", ad+"/apikeys/contract", "", ada, 403)
	req("DELETE", ad+"/users/"+dan, "", key, 204)
	req("DELETE", ad+"/users/nope", "", key, 404)

	// Documents. "posts" has rules, "private" has none (API keys only).
	p, priv := posts, "/v1/acme/app/private"
	doc := req("POST", p, `{"title": "hello", "published": true}`, ada, 201)
	id := p + "/" + doc["id"].(string)
	req("POST", p, `{"published": true}`, ada, 400)
	req("POST", p, `{"title": "x"}`, nil, 401)
	req("POST", p, `{"title": "x"}`, carl, 403)
	req("POST", "/v1/acme/app/nope", `{"title": "x"}`, key, 404)
	req("POST", p, `{"title": "x"}`, with(key, "Content-Type", "text/plain"), 415)
	f.store.fail = errConflictForContract
	req("POST", p, `{"title": "x"}`, key, 409)
	f.store.fail = nil

	req("GET", p+"?count=true", "", nil, 200)
	req("GET", p+"?where={bad", "", nil, 400)
	req("GET", priv, "", nil, 401)
	req("GET", priv, "", ada, 403)
	req("GET", "/v1/acme/app/nope", "", nil, 404)

	req("GET", id, "", nil, 200)
	req("GET", priv+"/x", "", nil, 401)
	req("GET", priv+"/x", "", ada, 403)
	req("GET", p+"/nope", "", ada, 404)

	for _, m := range []string{"PUT", "PATCH"} {
		body := `{"title": "hello", "published": true}`
		req(m, id, body, ada, 200)
		req(m, id, `{"title": 5}`, ada, 400)
		req(m, id, body, nil, 401)
		req(m, id, body, bearer(f.bob), 403)
		req(m, p+"/nope", body, ada, 404)
		req(m, id, body, with(ada, "If-Match", `"999"`), 412)
		f.store.fail = errConflictForContract
		req(m, id, body, key, 409)
		f.store.fail = nil
	}
	req("PATCH", id, `{"title": "x"}`, with(ada, "Content-Type", "application/json"), 415)

	req("DELETE", id, "", with(key, "If-Match", "3"), 400)
	req("DELETE", id, "", nil, 401)
	req("DELETE", id, "", ada, 403)
	req("DELETE", id, "", with(key, "If-Match", `"999"`), 412)
	// A delete checked against rules is conditional on the version it
	// read; losing every race against other writers gives up with 409.
	if err := f.svc.AddRole(context.Background(), "ada@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	docID := doc["id"].(string)
	f.store.beforeWrite = func(docs map[string]storage.Document) {
		d := docs[docID]
		d["_meta"].(map[string]any)["version"] = version(d) + 1
	}
	req("DELETE", id, "", ada, 409)
	f.store.beforeWrite = nil
	req("DELETE", id, "", key, 204)
	req("DELETE", id, "", key, 404)

	// Batch (roadmap F7): atomic writes across a database's collections.
	b := "/v1/acme/app/_batch"
	batchOK := req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "batch"}}]}`, ada, 200)
	batchID := batchOK["results"].([]any)[0].(map[string]any)["id"].(string)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts"}]}`, ada, 400)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, nil, 401)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, carl, 403)
	req("POST", b, `{"operations": [{"op": "create", "collection": "nope", "document": {}}]}`, key, 404)
	f.store.fail = errConflictForContract
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, key, 409)
	f.store.fail = nil
	req("POST", b, `{"operations": [{"op": "patch", "collection": "posts", "id": "`+batchID+`", "patch": {"title": "y"}, "if_match": "\"999\""}]}`, ada, 412)

	c.verify(t, router)
}

// TestContractDetectsViolations proves the contract checks catch broken
// responses, so a passing TestContract means something.
func TestContractDetectsViolations(t *testing.T) {
	c := loadContract(t)
	jsonHdr := func(extra ...string) http.Header {
		h := http.Header{"Content-Type": {"application/json"}}
		for i := 0; i+1 < len(extra); i += 2 {
			h.Set(extra[i], extra[i+1])
		}
		return h
	}
	doc := `{"id": "d1", "_meta": {"created_at": "2026-09-26T12:00:00.000Z", "updated_at": "2026-09-26T12:00:00.000Z", "version": 1}, "title": "x"}`
	docPath := "/v1/{realm}/{database}/{collection}/{id}"

	// A valid response passes.
	if err := c.check("GET", docPath, 200, jsonHdr("ETag", `"1"`, "Vary", "Origin, Authorization", "Cache-Control", "private, no-cache"), []byte(doc)); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	for _, tt := range []struct {
		name, method, path string
		status             int
		hdr                http.Header
		body, want         string
	}{
		{"error without request_id", "GET", docPath, 404, jsonHdr("X-Request-ID", "r"), `{"error": {"code": "not_found", "message": "x"}}`, "request_id"},
		{"error without X-Request-ID", "GET", docPath, 404, jsonHdr(), `{"error": {"code": "not_found", "message": "x", "request_id": "r"}}`, "X-Request-ID"},
		{"malformed error code", "GET", docPath, 404, jsonHdr("X-Request-ID", "r"), `{"error": {"code": "Not A Code", "message": "x", "request_id": "r"}}`, "code"},
		{"undocumented status", "GET", "/healthz", 418, jsonHdr(), `{}`, "not documented"},
		{"missing ETag", "GET", docPath, 200, jsonHdr(), doc, "ETag"},
		{"malformed ETag", "GET", docPath, 200, jsonHdr("ETag", "1"), doc, "ETag"},
		{"bad timestamp", "GET", docPath, 200, jsonHdr("ETag", `"1"`), strings.Replace(doc, "2026-09-26T12:00:00.000Z", "yesterday", 1), "date-time"},
		{"missing _meta", "GET", docPath, 200, jsonHdr("ETag", `"1"`), `{"id": "d1"}`, "_meta"},
		{"data without Vary", "GET", docPath, 200, jsonHdr("ETag", `"1"`), doc, "Vary"},
		{"data not varying on Authorization", "GET", docPath, 200, jsonHdr("ETag", `"1"`, "Vary", "Origin"), doc, "Vary"},
		{"data cacheable by shared caches", "GET", docPath, 200, jsonHdr("ETag", `"1"`, "Vary", "Origin, Authorization", "Cache-Control", "public, max-age=60"), doc, "Cache-Control"},
		{"extra user field", "GET", "/v1/{realm}/_auth/me", 200, jsonHdr("Cache-Control", "no-store"),
			`{"id": "u", "email": "a@x.io", "email_verified": false, "roles": [], "created_at": "2026-09-26T12:00:00.000Z", "password_hash": "x"}`, "password_hash"},
		{"cacheable account data", "GET", "/v1/{realm}/_auth/me", 200, jsonHdr(),
			`{"id": "u", "email": "a@x.io", "email_verified": false, "roles": [], "created_at": "2026-09-26T12:00:00.000Z"}`, "Cache-Control"},
		{"wrong content type", "GET", "/healthz", 200, http.Header{"Content-Type": {"text/plain"}}, `{"status": "ok"}`, "Content-Type"},
		{"body on 204", "DELETE", docPath, 204, http.Header{}, `{}`, "body not documented"},
		{"401 without WWW-Authenticate", "GET", "/v1/{realm}/_auth/me", 401, jsonHdr(), `{"error": {"code": "unauthenticated", "message": "x", "request_id": "r"}}`, "WWW-Authenticate"},
		{"unknown route", "GET", "/v1/{realm}/_auth/nope", 200, jsonHdr(), `{}`, "not in the spec"},
	} {
		err := c.check(tt.method, tt.path, tt.status, tt.hdr, []byte(tt.body))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tt.name, err, tt.want)
		}
	}
}
