package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/pquerna/otp/totp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/oauth/oauthtest"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
	"github.com/fernandezvara/backd/internal/storage/storagetest"
	"github.com/fernandezvara/backd/internal/triggers"
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
	idp := oauthtest.New(t)
	oauthSvc := idp.Service()
	live := oauthSvc.Endpoints
	oauthSvc.Endpoints = func(p *registry.Provider) oauth.Endpoints { // a provider whose keys can't be fetched
		e := live(p)
		if p.ClientID == "dead-client" {
			e.JWKS = "http://127.0.0.1:1/keys"
		}
		return e
	}
	f := newRulesFixture(t, func(cfg *Config) { cfg.OAuth = oauthSvc; cfg.Triggers = &fakeTriggerAdmin{}; cfg.Realtime = testHub(t) })
	setUpProviders(t, f)
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

	// Public caching: anonymous reads of an opted-in collection are public and revalidate.
	{
		const news = "/v1/acme/app/news"
		id := req("POST", news, `{"title": "t", "published": true}`, ada, 201)["id"].(string)
		prec, _ := f.doH(t, "GET", news+"/"+id, "", nil)
		if prec.Header().Get("Cache-Control") != "public, max-age=60" {
			t.Errorf("public document: %v", prec.Header())
		}
		req("GET", news+"/"+id, "", map[string]string{"If-None-Match": prec.Header().Get("ETag")}, 304)
		req("GET", news, "", nil, 200)
		req("GET", news, "", ada, 200)
	}

	// Reference expansion: notes reference the archive through their status.
	{
		aid := req("POST", "/v1/acme/app/archive", `{"title":"a"}`, ada, 201)["id"].(string)
		nid := req("POST", "/v1/acme/app/notes", `{"title":"n","status":"`+aid+`"}`, ada, 201)["id"].(string)
		out := req("GET", "/v1/acme/app/notes/"+nid+"?expand=status(title)", "", ada, 200)
		if out["_expanded"].(map[string]any)["status"].(map[string]any)["title"] != "a" {
			t.Errorf("expanded = %v", out)
		}
		req("GET", "/v1/acme/app/notes?expand=status", "", ada, 200)
		req("GET", "/v1/acme/app/notes/"+nid+"?expand=nope", "", ada, 400)
		req("GET", "/v1/acme/app/notes?expand=status.nope", "", ada, 400)
	}

	// Aggregations: the sales collection declares what can be grouped and measured.
	{
		const s = "/v1/acme/app/sales"
		req("POST", s, `{"category": "shoes", "total": 100.1, "paid_at": "2026-09-30T22:30:00Z"}`, ada, 201)
		req("POST", s, `{"category": "hats", "total": 20}`, ada, 201)
		const q = "/_aggregate?group_by=category,paid_at:month&tz=Europe/Madrid&metrics=count,sum:total,avg:total,min:paid_at,count_distinct:category&order_by=-count"
		out := req("GET", s+q, "", ada, 200)
		if out["total_groups"] != float64(2) {
			t.Errorf("aggregate = %v", out)
		}
		req("GET", s+"/_aggregate", "", ada, 200)
		req("GET", s+"/_aggregate?group_by=total", "", ada, 400)
		req("GET", "/v1/acme/app/notes/_aggregate", "", ada, 400)
		req("GET", s+"/_aggregate", "", nil, 401)
		req("GET", "/v1/acme/app/private/_aggregate", "", ada, 403)
		first, _ := f.doH(t, "GET", s+q, "", ada)
		req("GET", s+q, "", with(ada, "If-None-Match", first.Header().Get("ETag")), 304)
		f.store.aggregateErr = storage.ErrAggregateTooExpensive
		req("GET", s+"/_aggregate?group_by=category", "", ada, 422)
		f.store.aggregateErr = nil
		busy := "aggregate/acme/app/sales/user:" + f.adaID
		if !limiterFor(f.reg).tryAcquire(busy, 1) {
			t.Fatal("limiter busy")
		}
		req("GET", s+"/_aggregate", "", ada, 429)
		limiterFor(f.reg).release(busy, 1)
	}

	// Full-text search: a list, relevance, the aggregate and the rebuild window.
	{
		const a = "/v1/acme/app/articles"
		req("POST", a, `{"title": "Running shoes", "lang": "en"}`, ada, 201)
		out := req("GET", a+`?where={"$search":"running"}&order_by=_relevance`, "", ada, 200)
		if len(out["items"].([]any)) != 1 {
			t.Errorf("search = %v", out)
		}
		req("GET", a+`/_aggregate?where={"$search":"running"}&language=en`, "", ada, 200)
		req("GET", "/v1/acme/app/notes"+`?where={"$search":"x"}`, "", ada, 400)
		f.store.fail = storage.ErrSearchTooExpensive
		req("GET", a+`?where={"$search":"running"}`, "", ada, 422)
		f.store.fail = storage.ErrIndexBuilding
		req("GET", a+`?where={"$search":"running"}`, "", ada, 503)
		f.store.fail = nil
		f.store.aggregateErr = storage.ErrIndexBuilding
		req("GET", a+`/_aggregate?where={"$search":"running"}`, "", ada, 503)
		f.store.aggregateErr = nil
	}

	// Soft delete: delete, trash views, restore and purge.
	{
		const b = "/v1/acme/app/bin"
		id := req("POST", b, `{"title":"b"}`, ada, 201)["id"].(string)
		req("DELETE", b+"/"+id, "", ada, 204)
		req("GET", b+"?deleted=only", "", ada, 200)
		req("GET", b+"/"+id+"?deleted=only", "", ada, 200)
		req("GET", b+"?deleted=bogus", "", ada, 400)
		req("DELETE", b+"/"+id+"?purge=maybe", "", ada, 400)
		req("DELETE", b+"/"+id+"?purge=true", "", ada, 403)
		req("POST", b+"/nope/restore", "", ada, 404)
		req("POST", b+"/"+id+"/restore", "", nil, 401)
		req("POST", b+"/"+id+"/restore", "", with(ada, "If-Match", "bad"), 400)
		req("POST", b+"/"+id+"/restore", "", with(ada, "If-Match", `"9"`), 412)
		req("POST", b+"/"+id+"/restore", "", ada, 200)
		// No restore rule: its trash is closed.
		const a = "/v1/acme/app/archive"
		aid := req("POST", a, `{"title":"a"}`, ada, 201)["id"].(string)
		req("DELETE", a+"/"+aid, "", ada, 204)
		req("GET", a+"?deleted=only", "", ada, 403)
		req("POST", a+"/"+aid+"/restore", "", ada, 403)
	}

	// Custom ids: the id is the document's, checked before the rule and the insert; repeated
	// duplicates are held.
	{
		const inv = "/v1/acme/app/invoices"
		req("POST", inv, `{"title":"a"}`, ada, 400)
		req("POST", inv, `{"id":"nope","title":"a"}`, ada, 400)
		req("POST", "/v1/acme/app/posts", `{"id":"mine","title":"a"}`, ada, 400)
		req("POST", inv, `{"id":"INV-0001","title":"a"}`, ada, 201)
		req("POST", inv, `{"id":"INV-9999","title":"a"}`, ada, 403)
		for i := 0; i < 3; i++ {
			req("POST", inv, `{"id":"INV-0001","title":"a"}`, ada, 409)
		}
		req("POST", inv, `{"id":"INV-0002","title":"a"}`, ada, 429)
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
	req("POST", a+"/signup", `{"email": "pending@example.com", "password": "dev-p4ssw0rd!"}`, nil, 202) // registered: the same answer
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

	// Password reset.
	// The token belongs to a user nothing else in this test uses: a reset ends all sessions.
	resetter, _, err := f.svc.Signup(context.Background(), "resetter@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	resetToken := func() string {
		tok, _, err := f.svc.NewEmailToken(context.Background(), "reset-password", resetter.User.ID, "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	req("POST", a+"/reset-password/request", `{"email": "bob@example.com", "redirect_to": "https://app.acme.example/signin"}`, nil, 202)
	req("POST", a+"/reset-password/request", `{"email": "nobody@example.com"}`, nil, 202)
	req("POST", a+"/reset-password/request", `{"email": "nope"}`, nil, 400)
	req("POST", a+"/reset-password/request", `{"email": "bob@example.com", "redirect_to": "https://evil.example/"}`, nil, 400)
	req("POST", a+"/reset-password/request", `{"email": "a@example.com"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/reset-password/request", `{"email": "a@example.com"}`, nil, 404)
	for range 5 {
		req("POST", a+"/reset-password/request", `{"email": "limited@example.com"}`, nil, 202)
	}
	req("POST", a+"/reset-password/request", `{"email": "limited@example.com"}`, nil, 429)
	req("GET", a+"/reset-password?token="+resetToken(), "", nil, 200)
	req("GET", a+"/reset-password?token=nope", "", nil, 400)
	req("GET", "/v1/nope/_auth/reset-password?token=x", "", nil, 404)
	rt := resetToken()
	req("POST", a+"/reset-password", "token="+rt+"&password=short&password_confirm=short", form, 400)
	req("POST", a+"/reset-password", `{"token": "`+rt+`", "password": "short"}`, nil, 400)
	req("POST", a+"/reset-password", "token="+rt+"&password=dev-p4ssw0rd!2&password_confirm=dev-p4ssw0rd!2", form, 200)
	req("POST", a+"/reset-password", `{"token": "`+resetToken()+`", "password": "dev-p4ssw0rd!2"}`, nil, 204)
	req("POST", a+"/reset-password", `{"token": "nope", "password": "dev-p4ssw0rd!2"}`, nil, 400)
	req("POST", a+"/reset-password", `{"token": "x"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/reset-password", `{"token": "x", "password": "y"}`, nil, 404)

	// proofOf is the headers of a request a sensitive action accepts: the session of tok and a
	// fresh proof of it with the password.
	proofOf := func(tok, password string) map[string]string {
		p, err := f.svc.Authenticate(context.Background(), tok)
		if err != nil {
			t.Fatal(err)
		}
		g, err := f.svc.Reauthenticate(context.Background(), p, auth.ReauthPassword, password, "")
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"Authorization": "Bearer " + tok, reauthHeader: g.Token}
	}
	// Email change, undo and emailed invitations.
	cctx := context.Background()
	f.svc.Settings.Account.AllowEmailChange = true
	changerP, changerTok, err := f.svc.Signup(cctx, "changer@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	changer := proofOf(changerTok, "dev-p4ssw0rd!")
	req("POST", a+"/email", `{"new_email": "changer.new@example.com", "redirect_to": "https://app.acme.example/account"}`, changer, 202)
	req("POST", a+"/email", `{"new_email": "changer@example.com"}`, changer, 400)
	req("POST", a+"/email", `{"new_email": "changer.new@example.com"}`, nil, 401)
	req("POST", a+"/email", `{"new_email": "changer.new@example.com"}`, bearer(changerTok), 401)
	req("POST", a+"/email", `{"new_email": "x@example.com"}`, with(changer, "Content-Type", "text/plain"), 415)
	req("POST", "/v1/nope/_auth/email", `{"new_email": "x@example.com"}`, changer, 404)
	req("POST", a+"/email", `{"new_email": "changer.new@example.com"}`, changer, 202)
	req("POST", a+"/email", `{"new_email": "changer.new@example.com"}`, changer, 202)
	req("POST", a+"/email", `{"new_email": "changer.new@example.com"}`, changer, 429)
	changeToken := func(address, purpose string) string {
		tok, _, err := f.svc.NewEmailTokenFor(cctx, auth.EmailToken{Purpose: purpose, UserID: changerP.User.ID, Address: address}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	setAddresses := func(email, pending, previous string) {
		if err := f.svc.Store.UpdateUser(cctx, changerP.User.ID, auth.UserUpdate{Email: &email, PendingEmail: &pending, PreviousEmail: &previous}, *f.clock); err != nil {
			t.Fatal(err)
		}
	}
	setAddresses("changer@example.com", "cpending@example.com", "")
	req("GET", a+"/confirm-email-change?token="+changeToken("cpending@example.com", "change-email"), "", nil, 200)
	req("GET", a+"/confirm-email-change?token=nope", "", nil, 400)
	req("GET", "/v1/nope/_auth/confirm-email-change?token=x", "", nil, 404)
	req("POST", a+"/confirm-email-change", "token=nope", form, 400)
	req("POST", a+"/confirm-email-change", `{"token": "nope"}`, nil, 400)
	req("POST", a+"/confirm-email-change", "token="+changeToken("cpending@example.com", "change-email"), form, 200)
	setAddresses("changer@example.com", "pending2@example.com", "")
	req("POST", a+"/confirm-email-change", `{"token": "`+changeToken("pending2@example.com", "change-email")+`"}`, nil, 204)
	req("POST", a+"/confirm-email-change", `{"token": "x"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/confirm-email-change", `{"token": "x"}`, nil, 404)
	setAddresses("changed@example.com", "", "changer@example.com")
	req("GET", a+"/revert-email-change?token="+changeToken("changer@example.com", "revert-email-change"), "", nil, 200)
	req("GET", a+"/revert-email-change?token=nope", "", nil, 400)
	req("GET", "/v1/nope/_auth/revert-email-change?token=x", "", nil, 404)
	req("POST", a+"/revert-email-change", "token=nope", form, 400)
	req("POST", a+"/revert-email-change", `{"token": "nope"}`, nil, 400)
	req("POST", a+"/revert-email-change", "token="+changeToken("changer@example.com", "revert-email-change"), form, 200)
	setAddresses("changed@example.com", "", "changer@example.com")
	req("POST", a+"/revert-email-change", `{"token": "`+changeToken("changer@example.com", "revert-email-change")+`"}`, nil, 204)
	req("POST", a+"/revert-email-change", `{"token": "x"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/revert-email-change", `{"token": "x"}`, nil, 404)
	f.svc.Settings.Account.AllowEmailChange = false
	req("GET", a+"/confirm-email-change?token=x", "", nil, 404)

	invites := 0
	inviteToken := func() string {
		invites++
		inv, err := f.svc.SendInvitation(cctx, fmt.Sprintf("invited%d@example.com", invites), 0, "test", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := f.svc.NewEmailTokenFor(cctx, auth.EmailToken{Purpose: "invitation", InvitationID: inv.ID, Address: inv.Email}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	req("GET", a+"/accept-invitation?token="+inviteToken(), "", nil, 200)
	req("GET", a+"/accept-invitation?token=nope", "", nil, 400)
	req("GET", "/v1/nope/_auth/accept-invitation?token=x", "", nil, 404)
	req("POST", a+"/accept-invitation", "token=nope&password=dev-p4ssw0rd!&password_confirm=dev-p4ssw0rd!", form, 400)
	req("POST", a+"/accept-invitation", `{"token": "nope", "password": "dev-p4ssw0rd!"}`, nil, 400)
	req("POST", a+"/accept-invitation", "token="+inviteToken()+"&password=dev-p4ssw0rd!&password_confirm=dev-p4ssw0rd!", form, 200)
	req("POST", a+"/accept-invitation", `{"token": "`+inviteToken()+`", "password": "dev-p4ssw0rd!", "locale": "es"}`, nil, 204)
	req("POST", a+"/accept-invitation", `{"token": "x"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	req("POST", "/v1/nope/_auth/accept-invitation", `{"token": "x", "password": "y"}`, nil, 404)

	login := req("POST", a+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)
	me := bearer(login["token"].(string))
	req("POST", a+"/login", `{"email": "new@example.com"}`, nil, 400)
	// A session in a cookie: the answer has no token; without an allowed origin it is refused.
	cookieLogin := `{"email": "new@example.com", "password": "dev-p4ssw0rd!", "cookie": true}`
	if out := req("POST", a+"/login", cookieLogin, map[string]string{"Origin": "https://app.acme.example"}, 200); out["token"] != nil {
		t.Errorf("a cookie login returned a token: %v", out)
	}
	req("POST", a+"/login", cookieLogin, nil, 403)
	for range 5 {
		req("POST", a+"/login", `{"email": "victim@example.com", "password": "dev-p4ssw0rd!0"}`, nil, 401)
	}
	req("POST", a+"/login", `{"email": "victim@example.com", "password": "dev-p4ssw0rd!0"}`, nil, 429)

	// Sign-in with providers: the redirect flow end to end, and each way it can stop.
	{
		verifier, challenge, _ := auth.NewPKCE()
		o := a + "/oauth"
		redirect := url.QueryEscape("https://app.acme.example/signed-in")
		start := o + "/google/start?redirect_to=" + redirect + "&code_challenge=" + challenge
		idp.SetUser(map[string]any{"iss": "https://accounts.google.com", "aud": "g-client", "sub": "g-contract", "email": "oauth@example.com", "email_verified": true})
		rec, _ := f.doH(t, "GET", start, "", nil)
		if rec.Code != 302 {
			t.Fatalf("oauth start: %d", rec.Code)
		}
		req("GET", start, "", nil, 302)
		req("GET", o+"/github/start?redirect_to="+redirect+"&code_challenge="+challenge, "", nil, 404)
		req("GET", o+"/google/start?redirect_to="+redirect, "", nil, 400)
		req("POST", o+"/google/start", `{"redirect_to": "https://app.acme.example/x", "code_challenge": "`+challenge+`"}`, nil, 200)
		req("POST", o+"/google/start", `{"redirect_to": "https://app.acme.example/x", "code_challenge": "`+challenge+`", "intent": "link"}`, nil, 401)
		req("POST", o+"/google/start", `{"redirect_to": "https://evil.example/x", "code_challenge": "`+challenge+`"}`, nil, 400)
		req("POST", "/v1/nope/_auth/oauth/google/start", `{"redirect_to": "https://app.acme.example/x", "code_challenge": "`+challenge+`"}`, nil, 404)
		// The provider sends the user back; the app redeems the code.
		back, _ := url.Parse(idp.Authorize(rec.Header().Get("Location")))
		cb, _ := f.doH(t, "GET", back.Path+"?"+back.RawQuery, "", nil)
		to, _ := url.Parse(cb.Header().Get("Location"))
		if cb.Code != 302 || to.Query().Get("backd_code") == "" {
			t.Fatalf("oauth callback: %d %v", cb.Code, to)
		}
		req("POST", a+"/token", `{"code": "`+to.Query().Get("backd_code")+`", "code_verifier": "`+verifier+`"}`, nil, 200)
		req("POST", a+"/token", `{"code": "`+to.Query().Get("backd_code")+`", "code_verifier": "`+verifier+`"}`, nil, 401)
		req("POST", a+"/token", `{"code": "x"}`, nil, 400)
		req("POST", a+"/token", `{"code": "x", "code_verifier": "y", "cookie": true}`, map[string]string{"Origin": "https://evil.example"}, 403)
		// A state that leads nowhere is a page; Apple's answer is a form post.
		req("GET", o+"/google/callback?state=nope&code=x", "", nil, 400)
		req("POST", o+"/apple/callback", "state=nope&code=x", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 400)
		req("GET", "/v1/nope/_auth/oauth/google/callback?state=x", "", nil, 404)
		req("POST", "/v1/nope/_auth/oauth/google/callback", "state=x", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 404)
		idp.SetUser(map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.web", "sub": "a-contract", "email": "apple@example.com", "email_verified": "true"})
		arec, _ := f.doH(t, "GET", o+"/apple/start?redirect_to="+redirect+"&code_challenge="+challenge, "", nil)
		aback, _ := url.Parse(idp.Authorize(arec.Header().Get("Location")))
		if p := req("POST", aback.Path, aback.RawQuery, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 303); p != nil {
			t.Errorf("apple callback body: %v", p)
		}
		// Native sign-in with the ID token the app holds.
		native := func(claims map[string]any, extra string) string {
			return `{"id_token": "` + idp.IDToken(t, claims) + `", "nonce": "n1"` + extra + `}`
		}
		nativeClaims := func(sub, email string, verified bool) map[string]any {
			return map[string]any{"iss": "https://accounts.google.com", "aud": "g-native", "sub": sub, "email": email, "email_verified": verified, "nonce": "n1"}
		}
		ex := o + "/google/id-token"
		req("POST", ex, native(nativeClaims("g-n", "native@example.com", true), ""), nil, 200)
		req("POST", ex, native(nativeClaims("g-n", "native@example.com", true), `, "cookie": true`), map[string]string{"Origin": "https://evil.example"}, 403)
		req("POST", ex, native(nativeClaims("g-n2", "ada@example.com", false), ""), nil, 409)
		req("POST", ex, native(nativeClaims("g-n3", "", true), ""), nil, 400)
		req("POST", ex, `{"id_token": "x"}`, nil, 400)
		req("POST", ex, `{"id_token": "bad", "nonce": "n1"}`, nil, 401)
		req("POST", ex, native(nativeClaims("g-n", "native@example.com", true), `, "intent": "link"`), nil, 401)
		req("POST", ex, native(nativeClaims("g-link", "x@gmail.example", true), `, "intent": "link"`), ada, 200)
		req("POST", ex, native(nativeClaims("g-link", "x@gmail.example", true), `, "intent": "link"`), bearer(f.bob), 409)
		req("POST", o+"/github/id-token", native(nativeClaims("g-n", "n@example.com", true), ""), nil, 404)
		f.svc.Settings.Signup = registry.SignupClosed
		req("POST", ex, native(nativeClaims("g-closed", "closed@example.com", true), ""), nil, 403)
		f.svc.Settings.Signup = registry.SignupOpen
		f.svc.Settings.Providers["dead"] = &registry.Provider{Name: "google", ClientID: "dead-client", ClientSecret: "GOOGLE_SECRET"}
		req("POST", o+"/dead/id-token", native(nativeClaims("g-n", "native@example.com", true), ""), nil, 503)
		// A provider whose secret was never set can't be offered.
		f.svc.Settings.Providers["unset"] = &registry.Provider{Name: "google", ClientID: "g-client", ClientSecret: "NEVER_SET"}
		req("GET", o+"/unset/start?redirect_to="+redirect+"&code_challenge="+challenge, "", nil, 503)
		req("POST", o+"/unset/start", `{"redirect_to": "https://app.acme.example/x", "code_challenge": "`+challenge+`"}`, nil, 503)
		// Too many starts from one address.
		for range 40 {
			if rec, _ := f.doH(t, "GET", start, "", nil); rec.Code == 429 {
				break
			}
		}
		req("GET", start, "", nil, 429)
		req("POST", o+"/google/start", `{"redirect_to": "https://app.acme.example/x", "code_challenge": "`+challenge+`"}`, nil, 429)
		req("POST", a+"/token", `{"code": "x", "code_verifier": "y"}`, nil, 401)
		for range 31 {
			f.doH(t, "POST", a+"/token", `{"code": "x", "code_verifier": "y"}`, map[string]string{"Content-Type": "application/json"})
		}
		req("POST", a+"/token", `{"code": "x", "code_verifier": "y"}`, nil, 429)
		for range 31 {
			f.doH(t, "POST", ex, `{"id_token": "bad", "nonce": "n1"}`, map[string]string{"Content-Type": "application/json"})
		}
		req("POST", ex, `{"id_token": "bad", "nonce": "n1"}`, nil, 429)
	}
	// A second factor: the login answers with a challenge, which the code turns into a session.
	{
		ctx := context.Background()
		pw := "dev-p4ssw0rd!"
		u, err := f.svc.Create(ctx, "second@example.com", &pw)
		if err != nil {
			t.Fatal(err)
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "acme", AccountName: u.Email})
		if err != nil {
			t.Fatal(err)
		}
		if f.svc.Cipher == nil {
			f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
		}
		sealed, _ := f.svc.SealToken(key.Secret())
		recovery := f.svc.Cipher.MAC("recovery", f.svc.Realm, u.ID, "ABCDEABCDE")
		if err := f.svc.Store.PutMFA(ctx, auth.MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: time.Now(), RecoveryCodes: []string{recovery}}); err != nil {
			t.Fatal(err)
		}
		login := `{"email": "second@example.com", "password": "dev-p4ssw0rd!"}`
		ch := req("POST", a+"/login", login, nil, 200)["challenge"].(string)
		req("POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "000000"}`, nil, 400)
		code, _ := totp.GenerateCode(key.Secret(), f.svc.Clock())
		req("POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "`+code+`"}`, nil, 200)
		req("POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "`+code+`"}`, nil, 400)
		req("POST", a+"/mfa/verify", `{"challenge": "x"}`, nil, 400)
		req("POST", a+"/mfa/verify", `{"challenge": "x", "method": "totp", "code": "1", "cookie": true}`, map[string]string{"Origin": "https://evil.example"}, 403)
		ch = req("POST", a+"/login", login, nil, 200)["challenge"].(string)
		req("POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "recovery_code", "code": "abcde-abcde"}`, nil, 200)
		req("POST", "/v1/nope/_auth/mfa/verify", `{"challenge": "x", "method": "totp", "code": "1"}`, nil, 404)
		// Setting a second factor up and managing it, as the signed-in user.
		pw2 := "dev-p4ssw0rd!"
		if _, err := f.svc.Create(ctx, "enroll2@example.com", &pw2); err != nil {
			t.Fatal(err)
		}
		tok := req("POST", a+"/login", `{"email": "enroll2@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)["token"].(string)
		mine := proofOf(tok, "dev-p4ssw0rd!")
		jsonMine := with(mine, "Content-Type", "application/json")
		req("GET", a+"/mfa", "", mine, 200)
		req("GET", a+"/mfa", "", nil, 401)
		setup := req("POST", a+"/mfa/totp/setup", "", mine, 200)
		req("POST", a+"/mfa/totp/setup", "", nil, 401)
		confirmBody := func(c string) string {
			return `{"setup_id": "` + setup["setup_id"].(string) + `", "code": "` + c + `"}`
		}
		req("POST", a+"/mfa/totp/confirm", confirmBody("000000"), jsonMine, 400)
		req("POST", a+"/mfa/totp/confirm", `{}`, jsonMine, 400)
		req("POST", a+"/mfa/totp/confirm", confirmBody("000000"), map[string]string{"Content-Type": "application/json"}, 401)
		good, _ := totp.GenerateCode(setup["secret"].(string), f.svc.Clock())
		req("POST", a+"/mfa/totp/confirm", confirmBody(good), jsonMine, 200)
		req("POST", a+"/mfa/totp/setup", "", mine, 409)
		req("POST", a+"/mfa/recovery-codes", "", mine, 200)
		req("POST", a+"/mfa/recovery-codes", "", nil, 401)
		f.svc.Settings.MFA.RequiredAll = true
		req("DELETE", a+"/mfa/totp", "", mine, 403)
		f.svc.Settings.MFA.RequiredAll = false
		req("DELETE", a+"/mfa/totp", "", nil, 401)
		req("DELETE", a+"/mfa/totp", "", mine, 204)
		req("DELETE", a+"/mfa/totp", "", mine, 409)
		req("POST", a+"/mfa/recovery-codes", "", mine, 409)
		f.svc.Settings.MFA.Off = true
		req("POST", a+"/mfa/totp/setup", "", mine, 403)
		req("POST", a+"/mfa/totp/confirm", `{"setup_id": "x", "code": "123456"}`, jsonMine, 403)
		f.svc.Settings.MFA.Off = false
		cipher := f.svc.Cipher
		f.svc.Cipher = nil
		req("POST", a+"/mfa/totp/setup", "", mine, 503)
		f.svc.Cipher = cipher
		// A required role without a second factor is asked to set one up, at sign-up too.
		// (Without email, so that the verification message doesn't spend the address's budget.)
		f.svc.Settings.MFA.RequiredAll = true
		withEmail := f.svc.Settings.Email
		f.svc.Settings.Email = nil
		enrolling := req("POST", a+"/signup", `{"email": "enroll@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)["challenge"].(string)
		f.svc.Settings.Email = withEmail
		req("POST", a+"/login", `{"email": "enroll@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)
		// …which the challenge does, without a session: setup, then the first code.
		jsonOnly := map[string]string{"Content-Type": "application/json"}
		req("POST", a+"/mfa/totp/setup", `{"challenge": "nope"}`, jsonOnly, 400)
		cs := req("POST", a+"/mfa/totp/setup", `{"challenge": "`+enrolling+`"}`, jsonOnly, 200)
		cbody := func(c string) string {
			return `{"challenge": "` + enrolling + `", "setup_id": "` + cs["setup_id"].(string) + `", "code": "` + c + `"}`
		}
		req("POST", a+"/mfa/totp/confirm", cbody("000000"), jsonOnly, 400)
		req("POST", a+"/mfa/totp/confirm", `{"challenge": "nope", "setup_id": "x", "code": "123456"}`, jsonOnly, 400)
		first, _ := totp.GenerateCode(cs["secret"].(string), f.svc.Clock())
		done := req("POST", a+"/mfa/totp/confirm", cbody(first), jsonOnly, 200)
		if done["token"] == nil || done["recovery_codes"] == nil {
			t.Errorf("enrolling from a challenge: %v", done)
		}
		req("POST", a+"/mfa/totp/confirm", cbody(first), jsonOnly, 400)
		f.svc.Settings.MFA.RequiredAll = false
		// Ten wrong codes in a row lock the user for a while.
		for range 10 {
			ch = req("POST", a+"/login", login, nil, 200)["challenge"].(string)
			f.doH(t, "POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "000000"}`, map[string]string{"Content-Type": "application/json"})
		}
		ch = req("POST", a+"/login", login, nil, 200)["challenge"].(string)
		req("POST", a+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "000000"}`, nil, 429)
	}
	who := req("GET", a+"/me", "", me, 200)
	if ids, _ := who["identities"].([]any); len(ids) != 1 || ids[0].(map[string]any)["provider"] != "password" {
		t.Errorf("a password user's identities: %v", who["identities"])
	}
	// A user with a password and no other way in can't drop it; with a provider they can, once.
	{
		ctx := context.Background()
		pw := "dev-p4ssw0rd!"
		np, err := f.svc.Create(ctx, "nopass@example.com", &pw)
		if err != nil {
			t.Fatal(err)
		}
		tok := req("POST", a+"/login", `{"email": "nopass@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)["token"].(string)
		req("DELETE", a+"/password", "", bearer(tok), 401)
		req("DELETE", a+"/password", "", nil, 401)
		proof := proofOf(tok, pw)
		req("DELETE", a+"/password", "", proof, 409)
		if err := f.svc.Store.PutIdentity(ctx, auth.Identity{ID: "idn-np", UserID: np.ID, Provider: "google", Subject: "g-nopass", Email: "nopass@example.com", EmailVerified: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if got := req("GET", a+"/me", "", bearer(tok), 200)["sign_in_methods"].([]any); len(got) != 2 || got[0] != "password" || got[1] != "google" {
			t.Errorf("sign_in_methods: %v", got)
		}
		req("DELETE", a+"/password", "", proof, 204)
		req("DELETE", a+"/password", "", proof, 404)
		req("POST", a+"/password", `{"new_password": "dev-p4ssw0rd!5"}`, with(proof, "Content-Type", "application/json"), 204)
	}
	// Sign-in methods: the only one can't be removed; a second one can, and a method the user lacks is a 404.
	meOnce := proofOf(login["token"].(string), "dev-p4ssw0rd!")
	req("DELETE", a+"/identities/password", "", meOnce, 409)
	req("DELETE", a+"/identities/google", "", meOnce, 404)
	req("DELETE", a+"/identities/google", "", nil, 401)
	if err := f.svc.Store.PutIdentity(context.Background(), auth.Identity{ID: "idn1", UserID: who["id"].(string), Provider: "google", Subject: "g-1", Email: "g@example.com", EmailVerified: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if ids := req("GET", a+"/me", "", me, 200)["identities"].([]any); len(ids) != 2 {
		t.Errorf("identities after linking: %v", ids)
	}
	req("DELETE", a+"/identities/google", "", bearer(login["token"].(string)), 401)
	req("DELETE", a+"/identities/google", "", meOnce, 204)
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
	meProof := proofOf(login["token"].(string), "dev-p4ssw0rd!")
	req("POST", a+"/password", `{}`, with(meProof, "Content-Type", "application/json"), 400)
	req("POST", a+"/password", `{"new_password": "dev-p4ssw0rd!2"}`, with(me, "Content-Type", "application/json"), 401)
	req("POST", a+"/password", `{"new_password": "dev-p4ssw0rd!2"}`, with(meProof, "Content-Type", "application/json"), 204)
	req("DELETE", a+"/me", "", me, 401)
	// The proof of a password that no longer is theirs, and a method the user doesn't give.
	req("POST", a+"/reauth", `{"method": "password", "password": "dev-p4ssw0rd!0"}`, with(me, "Content-Type", "application/json"), 401)
	req("POST", a+"/reauth", `{"method": "email_code", "code": "123456"}`, with(me, "Content-Type", "application/json"), 400)
	req("POST", a+"/reauth", `{"method": "sms"}`, with(me, "Content-Type", "application/json"), 400)
	req("POST", a+"/reauth", `{"method": "password"}`, with(me, "Content-Type", "application/json"), 400)
	req("POST", a+"/reauth", `{"method": "password", "password": "dev-p4ssw0rd!2"}`, nil, 401)
	req("POST", a+"/reauth", `{"method": "password", "password": "dev-p4ssw0rd!2"}`, with(me, "Content-Type", "application/json"), 200)
	req("POST", a+"/reauth/email-code", "", me, 400)
	req("POST", a+"/reauth/email-code", "", nil, 401)
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
	req("DELETE", a+"/me", "", proofOf(fourth["token"].(string), "dev-p4ssw0rd!2"), 204)

	// Admin: every operation refuses callers without a key.
	ad := admin
	adminOps := [][3]string{
		{"GET", "/users", ""}, {"POST", "/users", `{"email": "z@example.com"}`},
		{"GET", "/users/" + f.bobID, ""}, {"GET", "/users/" + f.bobID + "/owned", ""}, {"PATCH", "/users/" + f.bobID, `{"disabled": false}`},
		{"DELETE", "/users/" + f.bobID, ""}, {"POST", "/users/" + f.bobID + "/password", `{"password": "dev-p4ssw0rd!"}`},
		{"PUT", "/users/" + f.bobID + "/roles/admin", ""}, {"DELETE", "/users/" + f.bobID + "/roles/admin", ""},
		{"POST", "/users/" + f.bobID + "/email", `{"email": "z@example.com"}`},
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
	if page := req("GET", ad+"/users?limit=1", "", key, 200); page["has_more"] == true && page["next_cursor"] != nil {
		req("GET", ad+"/users?limit=1&after="+page["next_cursor"].(string), "", key, 200)
	} else {
		t.Errorf("a first page of users has a next_cursor: %v", page)
	}
	req("GET", ad+"/users?after=a@example.com&skip=1", "", key, 400)
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
	req("GET", ad+"/users/"+dan+"/owned", "", key, 200)
	req("GET", ad+"/users/nope/owned", "", key, 404)
	req("DELETE", ad+"/users/"+dan+"/roles/admin", "", key, 200)
	req("DELETE", ad+"/users/nope/roles/admin", "", key, 404)
	inv := req("POST", ad+"/invitations", `{"email": "eve@example.com", "expires_in": "3d"}`, key, 201)
	req("POST", ad+"/invitations", `{}`, key, 201)
	req("POST", ad+"/invitations", `{"expires_in": "forever"}`, key, 400)
	req("POST", ad+"/invitations", `{"email": "sent@example.com", "send": true, "redirect_to": "https://app.acme.example/welcome", "locale": "es"}`, key, 201)
	req("POST", ad+"/invitations", `{"send": true}`, key, 400)
	req("POST", ad+"/invitations", `{"email": "sent2@example.com", "send": true, "redirect_to": "https://evil.example/"}`, key, 400)
	req("POST", "/v1/nope/_admin/invitations", `{"email": "x@example.com", "send": true}`, key, 404)
	for range 3 {
		req("POST", ad+"/invitations", `{"email": "limit@example.com", "send": true}`, key, 201)
	}
	req("POST", ad+"/invitations", `{"email": "limit@example.com", "send": true}`, key, 429)
	req("POST", ad+"/users/"+dan+"/email", `{"email": "dan.new@example.com"}`, key, 200)
	req("POST", ad+"/users/"+dan+"/email", `{"email": "dan.new@example.com"}`, key, 400)
	req("POST", ad+"/users/"+dan+"/email", `{"email": "ada@example.com"}`, key, 409)
	req("POST", ad+"/users/nope/email", `{"email": "x@example.com"}`, key, 404)
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
	req("GET", ad+"/config", "", key, 200)
	req("GET", ad+"/config", "", nil, 401)
	req("GET", ad+"/config", "", ada, 403)
	spec := "/v1/acme/_openapi.json"
	specDoc := req("GET", spec, "", key, 200)
	if rec, _ := f.doH(t, "GET", spec, "", key); rec.Header().Get("ETag") == "" {
		t.Error("the document has no ETag")
	} else {
		req("GET", spec, "", map[string]string{"Authorization": key["Authorization"], "If-None-Match": rec.Header().Get("ETag")}, 304)
	}
	req("GET", "/v1/nope/_openapi.json", "", key, 404)
	if specDoc["openapi"] != "3.1.0" {
		t.Errorf("openapi document = %v", specDoc["openapi"])
	}
	req("GET", spec, "", nil, 401)
	req("GET", spec, "", ada, 403)
	req("GET", ad+"/types", "", key, 200)
	adminSpec := req("GET", ad+"/openapi.json", "", key, 200)
	if adminSpec["openapi"] != "3.1.0" {
		t.Errorf("admin openapi document = %v", adminSpec["openapi"])
	}
	if rec, _ := f.doH(t, "GET", ad+"/openapi.json", "", key); rec.Header().Get("ETag") == "" {
		t.Error("the admin document has no ETag")
	} else {
		req("GET", ad+"/openapi.json", "", map[string]string{"Authorization": key["Authorization"], "If-None-Match": rec.Header().Get("ETag")}, 304)
	}
	req("GET", ad+"/openapi.json", "", nil, 401)
	req("GET", ad+"/openapi.json", "", ada, 403)
	req("GET", "/v1/nope/_admin/openapi.json", "", key, 404)
	req("GET", ad+"/types?db=nope", "", key, 400)
	req("GET", ad+"/types", "", nil, 401)
	req("GET", ad+"/types", "", ada, 403)
	req("GET", ad+"/users?q=ada", "", key, 200)
	req("GET", ad+"/users?q=", "", key, 400)
	// A user made for the purpose, so ending its session disturbs nobody else.
	temp, err := f.svc.Create(context.Background(), "sessions-contract@example.com", ptrString("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Login(context.Background(), temp.Email, "dev-p4ssw0rd!", "203.0.113.50"); err != nil {
		t.Fatal(err)
	}
	sessList := req("GET", ad+"/users/"+temp.ID+"/sessions", "", key, 200)
	req("GET", ad+"/users/"+temp.ID+"/sessions", "", nil, 401)
	req("GET", ad+"/users/"+temp.ID+"/sessions", "", ada, 403)
	req("GET", ad+"/users/nope/sessions", "", key, 404)
	req("DELETE", ad+"/users/"+temp.ID+"/sessions/nope", "", key, 404)
	req("DELETE", ad+"/users/"+temp.ID+"/sessions/x", "", nil, 401)
	req("DELETE", ad+"/users/"+temp.ID+"/sessions/x", "", ada, 403)
	req("DELETE", ad+"/users/"+temp.ID+"/sessions/"+sessList["items"].([]any)[0].(map[string]any)["id"].(string), "", key, 204)
	// The user's sign-in methods.
	idents := req("GET", ad+"/users/"+temp.ID+"/identities", "", key, 200)
	if items, _ := idents["items"].([]any); len(items) != 1 || items[0].(map[string]any)["provider"] != "password" {
		t.Errorf("identities of a password user: %v", idents)
	}
	req("GET", ad+"/users/"+temp.ID+"/identities", "", nil, 401)
	req("GET", ad+"/users/"+temp.ID+"/identities", "", ada, 403)
	req("GET", ad+"/users/nope/identities", "", key, 404)
	// The user's second factor, and who must set one up.
	if st := req("GET", ad+"/users/"+temp.ID+"/mfa", "", key, 200); st["enabled"] != false || st["required"] != false {
		t.Errorf("mfa of a user with none: %v", st)
	}
	req("GET", ad+"/users/"+temp.ID+"/mfa", "", nil, 401)
	req("GET", ad+"/users/"+temp.ID+"/mfa", "", ada, 403)
	req("GET", ad+"/users/nope/mfa", "", key, 404)
	// Resetting it: one is made for the user, then removed; not twice, not for nobody, not for yourself.
	req("POST", ad+"/users/"+temp.ID+"/mfa/reset", "", key, 409)
	if err := f.svc.Store.PutMFA(context.Background(), auth.MFARecord{UserID: temp.ID, Secret: "k.n.c", EnabledAt: time.Now(), RecoveryCodes: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	req("POST", ad+"/users/"+temp.ID+"/mfa/reset", "", nil, 401)
	req("POST", ad+"/users/"+temp.ID+"/mfa/reset", "", ada, 403)
	req("POST", ad+"/users/nope/mfa/reset", "", key, 404)
	req("POST", ad+"/users/"+temp.ID+"/mfa/reset", "", key, 204)
	selfAdmin, err := f.svc.Create(context.Background(), "self-admin@example.com", ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AddRole(context.Background(), selfAdmin.Email, "staff"); err != nil {
		t.Fatal(err)
	}
	selfTok := req("POST", a+"/login", `{"email": "self-admin@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)["token"].(string)
	req("POST", ad+"/users/"+selfAdmin.ID+"/mfa/reset", "", bearer(selfTok), 403)
	f.svc.Settings.MFA.RequiredAll = true
	over := req("GET", ad+"/mfa", "", key, 200)
	if over["required_all"] != true || over["total_without_mfa"].(float64) < 1 || len(over["users_without_mfa"].([]any)) < 1 {
		t.Errorf("mfa overview: %v", over)
	}
	f.svc.Settings.MFA.RequiredAll = false
	req("GET", ad+"/mfa", "", nil, 401)
	req("GET", ad+"/mfa", "", ada, 403)
	req("DELETE", ad+"/users/"+temp.ID+"/identities/password", "", key, 409)
	req("DELETE", ad+"/users/"+temp.ID+"/identities/google", "", key, 404)
	req("DELETE", ad+"/users/"+temp.ID+"/identities/google", "", nil, 401)
	req("DELETE", ad+"/users/"+temp.ID+"/identities/google", "", ada, 403)
	req("DELETE", ad+"/users/nope/identities/google", "", key, 404)
	now := time.Now()
	if err := f.svc.Store.PutIdentity(context.Background(), auth.Identity{ID: "idn-admin", UserID: temp.ID, Provider: "google", Subject: "g-admin", Email: "g@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	req("DELETE", ad+"/users/"+temp.ID+"/identities/google", "", key, 204)
	req("GET", ad+"/whoami", "", key, 200)
	req("GET", ad+"/whoami", "", nil, 401)
	req("GET", ad+"/whoami", "", ada, 403)
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
	if out := req("GET", ad+"/secrets", "", key, 200); len(out["items"].([]any)) != 2+3 { // plus the provider secrets of setUpProviders
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
	// A sync function as a job, when the caller prefers it.
	req("POST", fn+"echo", `{"n": 1}`, with(ada, "Prefer", "respond-async"), 202)
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
	jobID := req("POST", run+"cleanup/invoke", `{"input": {"n": 1}}`, key, 202)["id"].(string)
	req("POST", run+"echo/invoke", `{"input": {"n": 1}, "as": "ada@example.com"}`, key, 200)
	req("POST", run+"echo/invoke", `{"as": "nobody@example.com"}`, key, 404)
	req("POST", run+"nothing_here/invoke", `{}`, key, 404)
	req("POST", run+"hook/invoke", `{}`, key, 400)
	req("POST", run+"typed/invoke", `{"input": {"n": "x"}}`, key, 400)
	req("POST", run+"echo/invoke", `{}`, nil, 401)
	req("POST", run+"echo/invoke", `{}`, with(ada, "Content-Type", "application/json"), 403)
	// The trigger dispatcher's state and gaps.
	req("GET", ad+"/triggers/status", "", key, 200)
	req("GET", ad+"/triggers/status", "", nil, 401)
	req("GET", ad+"/triggers/status", "", ada, 403)
	req("GET", ad+"/triggers/gaps", "", key, 200)
	req("GET", ad+"/triggers/gaps?all=true&limit=5", "", key, 200)
	req("GET", ad+"/triggers/gaps?limit=0", "", key, 400)
	req("GET", ad+"/triggers/gaps", "", nil, 401)
	req("GET", ad+"/triggers/gaps", "", ada, 403)
	req("POST", ad+"/triggers/gaps/gap1/resolve", ``, nil, 401)
	req("POST", ad+"/triggers/gaps/gap1/resolve", ``, ada, 403)
	req("POST", ad+"/triggers/gaps/nothing/resolve", ``, key, 404)
	req("POST", ad+"/triggers/gaps/gap1/resolve", ``, key, 200)
	// Pausing and resuming schedules.
	req("GET", ad+"/schedules", "", key, 200)
	req("GET", ad+"/schedules", "", nil, 401)
	req("GET", ad+"/schedules", "", ada, 403)
	req("POST", run+"nightly/pause", ``, key, 200)
	req("POST", run+"nightly/resume", ``, key, 200)
	req("POST", run+"echo/pause", ``, key, 409) // not scheduled
	req("POST", run+"nothing_here/pause", ``, key, 404)
	req("POST", run+"nightly/pause", ``, nil, 401)
	req("POST", run+"nightly/pause", ``, ada, 403)
	req("POST", run+"nightly/resume", ``, nil, 401)
	req("POST", run+"nightly/resume", ``, ada, 403)
	req("POST", run+"echo/resume", ``, key, 409)
	req("POST", run+"nothing_here/resume", ``, key, 404)
	// The storage check: the realm has none, then one whose keys aren't set, then one that works.
	store := ad + "/storage/check"
	req("POST", store, ``, nil, 401)
	req("POST", store, ``, ada, 403)
	saved := f.reg.Realms["acme"].Settings.Storage
	f.reg.Realms["acme"].Settings.Storage = nil
	req("POST", store, ``, key, 404)
	f.reg.Realms["acme"].Settings.Storage = saved
	fake := storagetest.New(t, "files")
	f.reg.Realms["acme"].Settings.Storage = &registry.StorageSettings{Provider: "minio", Endpoint: fake.URL, Region: "us-east-1", Bucket: "files", Prefix: "contract", AccessKey: "STORAGE_ACCESS_KEY", SecretKey: "STORAGE_SECRET_KEY", Download: registry.DownloadPresigned, PresignedTTL: registry.DefaultPresignedTTL, PendingTTL: registry.DefaultPendingTTL, HTTP: true}
	req("POST", store, ``, key, 503)
	for _, name := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY"} {
		if err := f.svc.SetSecret(context.Background(), "", name, "value-"+name, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	if out := req("POST", store, ``, key, 200); out["ok"] != true || out["checksum_sha256"] != "verified" {
		t.Errorf("storage check: %v", out)
	}
	// The export of the realm: the plan, one export at a time, a collection's documents, a file's object.
	{
		exp := ad + "/export"
		req("GET", exp, "", nil, 401)
		req("GET", exp, "", ada, 403)
		req("DELETE", exp, "", nil, 401)
		req("DELETE", exp, "", ada, 403)
		pngData := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("x", 20)
		pu := req("POST", "/v1/acme/app/library/_files/avatar/uploads", pngData, with(key, "Content-Type", "image/png"), 201)
		doc := req("POST", "/v1/acme/app/library", `{"title": "exported", "avatar": {"upload": "`+pu["upload_id"].(string)+`", "token": "`+pu["upload_token"].(string)+`"}}`, key, 201)
		fileID := doc["avatar"].(map[string]any)["id"].(string)
		plan := req("GET", exp+"?database=app&collection=library", "", key, 200)
		lh := with(key, "X-Backd-Export", plan["lease"].(string))
		req("GET", exp, "", key, 409)
		req("GET", exp+"?database=app&collection=library&fields=ghost", "", key, 400)
		req("GET", exp+"?part=app/library.jsonl", "", key, 409)
		req("GET", exp+"?part=ghost/library.jsonl", "", lh, 404)
		req("GET", exp+"?part=app/library.jsonl", "", lh, 200)
		req("GET", exp+"?file=app/library/"+fileID, "", lh, 200)
		req("GET", exp+"?file=app/library/nothing", "", lh, 404)
		savedStorage := f.reg.Realms["acme"].Settings.Storage
		broken := *savedStorage
		broken.AccessKey = "EXPORT_KEY_NOT_SET"
		f.reg.Realms["acme"].Settings.Storage = &broken
		req("GET", exp+"?file=app/library/"+fileID, "", lh, 503)
		f.reg.Realms["acme"].Settings.Storage = savedStorage
		req("DELETE", exp+"?complete=true", "", lh, 200)
		req("DELETE", exp, "", lh, 409)
		req("GET", exp+"?include=auth&database=app", "", key, 400)
		req("POST", exp, `{"anonymize": true}`, nil, 401)
		req("POST", exp, `{"anonymize": true}`, ada, 403)
		req("POST", exp+"?database=app", `{"anonymize": true, "anonymize_config": "collections:\n  app/library:\n    remove: [nothing]\n"}`, key, 400)
		anon := req("POST", exp+"?database=app&collection=library", `{"anonymize": true}`, key, 200)
		req("POST", exp, `{"anonymize": true}`, key, 409)
		req("DELETE", exp, "", with(key, "X-Backd-Export", anon["lease"].(string)), 200)
		withAuth := req("GET", exp+"?include=auth", "", key, 200)
		if len(withAuth["export_key"].(string)) != 43 {
			t.Errorf("export key: %v", withAuth["export_key"])
		}
		req("DELETE", exp, "", with(key, "X-Backd-Export", withAuth["lease"].(string)), 200)
	}
	// Importing a package: the start, a chunk validated and written, a file, the end.
	{
		imp := ad + "/import"
		manifest := `{"manifest": {"format": 1, "realm": "acme", "databases": [{"name": "app", "collections": [{"name": "library", "part": "app/library.jsonl"}]}]}`
		req("POST", imp, manifest+`}`, nil, 401)
		req("POST", imp, manifest+`}`, ada, 403)
		req("PUT", imp+"?file=app/library/x", "x", nil, 401)
		req("DELETE", imp, "", nil, 401)
		req("DELETE", imp, "", ada, 403)
		req("PUT", imp+"?file=app/library/x", "x", ada, 403)
		req("POST", imp, `nope`, key, 400)
		req("POST", imp, `{"manifest": {"format": 9}}`, key, 422)
		started := req("POST", imp, manifest+`, "mode": "replace"}`, key, 200)
		ih := with(key, "X-Backd-Export", started["lease"].(string))
		req("POST", imp, manifest+`}`, key, 409)
		req("POST", imp+"?phase=validate&part=app/library.jsonl", "", with(key, "X-Backd-Export", "nope"), 409)
		req("POST", imp+"?phase=nothing&part=app/library.jsonl", "", ih, 400)
		req("POST", imp+"?phase=validate&part=app/ghost.jsonl", "", ih, 404)
		doc, _ := exportpkg.EncodeDocument(map[string]any{"id": "9m4e2mr0ui3e8a20014g", "title": "imported", "_meta": map[string]any{"version": int64(1), "created_at": time.Now().UTC(), "updated_at": time.Now().UTC()}})
		out := req("POST", imp+"?phase=validate&part=app/library.jsonl", string(doc)+"\n", with(ih, "Content-Type", "application/x-ndjson"), 200)
		if out["counts"].(map[string]any)["created"] != float64(1) {
			t.Errorf("validate: %v", out)
		}
		req("POST", imp+"?phase=write&part=app/library.jsonl", string(doc)+"\n", with(ih, "Content-Type", "application/x-ndjson"), 200)
		req("POST", imp+"?phase=validate&part=_system/auth/users.jsonl", `{"id":"u9","email":"nine@example.com","roles":[]}`+"\n", with(ih, "Content-Type", "application/x-ndjson"), 200)
		sum := sha256.Sum256([]byte("bytes"))
		req("PUT", imp+"?file=app/library/fl_0123456789abcdefghij", "bytes", with(ih, "X-Backd-Sha256", hex.EncodeToString(sum[:])), 204)
		req("PUT", imp+"?file=app/library/fl_0123456789abcdefghij", "bytes", ih, 400)
		req("PUT", imp+"?file=app/library/fl_0123456789abcdefghij", "bytes", with(with(key, "X-Backd-Export", "nope"), "X-Backd-Sha256", hex.EncodeToString(sum[:])), 409)
		req("PUT", imp+"?file=app/ghost/x", "bytes", with(ih, "X-Backd-Sha256", hex.EncodeToString(sum[:])), 404)
		ghost := f.reg.Realms["acme"].Settings.Storage
		broken := *ghost
		broken.AccessKey = "IMPORT_KEY_NOT_SET"
		f.reg.Realms["acme"].Settings.Storage = &broken
		req("PUT", imp+"?file=app/library/fl_0123456789abcdefghij", "bytes", with(ih, "X-Backd-Sha256", hex.EncodeToString(sum[:])), 503)
		f.reg.Realms["acme"].Settings.Storage = ghost
		req("DELETE", imp+"?complete=true", "", ih, 204)
		req("DELETE", imp, "", ih, 409)
		dry := req("POST", imp, manifest+`, "dry_run": true}`, key, 200)
		req("POST", imp+"?phase=write&part=app/library.jsonl", "", with(key, "X-Backd-Export", dry["lease"].(string)), 409)
		req("DELETE", imp, "", with(key, "X-Backd-Export", dry["lease"].(string)), 204)
	}
	// Files: upload, download, remove and clear, on the data route and the admin data route.
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("x", 20)
	typed := func(h map[string]string, ct string) map[string]string { return with(h, "Content-Type", ct) }
	carlOr := func(k map[string]string, admin bool, c map[string]string) map[string]string {
		if admin {
			return k
		}
		return c
	}
	for _, base := range []string{"/v1/acme/app/library", ad + "/data/app/library"} {
		isAdmin := strings.Contains(base, "_admin")
		info := req("GET", base+"/_files/avatar", "", key, 200)
		if info["upload"] != "proxy" || info["max_files"] != float64(1) || info["max_size"] != float64(4096) {
			t.Errorf("file field: %v", info)
		}
		req("GET", base+"/_files/receipts", "", key, 200)
		req("GET", base+"/_files/nope", "", key, 404)
		if isAdmin {
			req("GET", base+"/_files/avatar", "", nil, 401)
			req("GET", base+"/_files/avatar", "", ada, 403)
		}
		// A pending upload, and the document made with it.
		pend := base + "/_files/avatar/uploads"
		pu := req("POST", pend, png, typed(key, "image/png"), 201)
		req("POST", base, `{"title": "with a file", "avatar": {"upload": "`+pu["upload_id"].(string)+`", "token": "`+pu["upload_token"].(string)+`"}}`, key, 201)
		req("POST", base, `{"title": "bad ref", "avatar": {"upload": "`+pu["upload_id"].(string)+`", "token": "wrong"}}`, key, 400)
		req("POST", pend, png, typed(nil, "image/png"), 401)
		req("POST", pend, "plain text", typed(key, "text/plain"), 415)
		req("POST", pend, strings.Repeat("x", 5000), typed(key, "image/png"), 413)
		req("POST", base+"/_files/nope/uploads", png, typed(key, "image/png"), 404)
		cutJPEG := string(dirtyJPEGForContract(t))
		req("POST", pend[:len(pend)-len("/avatar/uploads")]+"/shot/uploads", cutJPEG, typed(key, "image/jpeg"), 422)
		if isAdmin {
			req("POST", pend, png, typed(ada, "image/png"), 403)
		} else {
			lib := f.reg.Realms["acme"].Databases["app"].Collections["library"]
			rl := lib.Rules
			lib.Rules = nil
			req("POST", pend, png, typed(ada, "image/png"), 403)
			lib.Rules = rl
		}
		saved := f.reg.Realms["acme"].Settings.Storage
		broken := *saved
		broken.AccessKey = "NOT_SET_ANYWHERE"
		f.reg.Realms["acme"].Settings.Storage = &broken
		req("POST", pend, png, typed(key, "image/png"), 503)
		f.reg.Realms["acme"].Settings.Storage = saved

		doc := req("POST", base, `{"title": "files"}`, key, 201)["id"].(string)
		files := base + "/" + doc + "/_files/"
		req("POST", files+"shot", cutJPEG, typed(key, "image/jpeg"), 422)
		up := req("POST", files+"avatar?name=face.png", png, typed(key, "image/png"), 201)
		avatar := up["avatar"].(map[string]any)["id"].(string)
		req("POST", files+"avatar", png, typed(with(key, "If-Match", "bad"), "image/png"), 400)
		req("POST", files+"avatar", png, typed(nil, "image/png"), 401)
		if isAdmin {
			req("POST", files+"avatar", png, typed(ada, "image/png"), 403)
		} else {
			mine := req("POST", base, `{"title": "mine"}`, ada, 201)["id"].(string)
			req("POST", base+"/"+mine+"/_files/thumbnail", png, typed(ada, "image/png"), 403)
		}
		req("POST", base+"/nope/_files/avatar", png, typed(key, "image/png"), 404)
		req("POST", files+"avatar", png, typed(with(key, "If-Match", `"999"`), "image/png"), 412)
		req("POST", files+"avatar", strings.Repeat("x", 5000), typed(key, "image/png"), 413)
		req("POST", files+"avatar", "plain text", typed(key, "text/plain"), 415)
		req("POST", files+"receipts", png, typed(key, "image/png"), 201)
		req("POST", files+"receipts", png, typed(key, "image/png"), 201)
		req("POST", files+"receipts", png, typed(key, "image/png"), 409)
		req("POST", files+"manual", "the manual", typed(key, "text/plain"), 201)
		manual := req("GET", base+"/"+doc, "", key, 200)["manual"].(map[string]any)["id"].(string)

		// Downloads: a redirect, the link as JSON, the bytes, a range, a repeat, and the refusals.
		req("GET", files+"avatar/"+avatar+"?link=json", "", key, 200)
		if rec, _ := f.doH(t, "GET", files+"avatar/"+avatar, "", key); rec.Code != 302 {
			t.Errorf("download redirect: %d", rec.Code)
		}
		mp := files + "manual/" + manual
		_, _ = f.doH(t, "GET", mp, "", key)
		etag := `"` + sha([]byte("the manual")) + `"`
		req("GET", mp, "", with(key, "If-None-Match", etag), 304)
		req("GET", mp, "", with(key, "Range", "bytes=0-2"), 206)
		req("GET", mp, "", with(key, "Range", "bytes=500-600"), 416)
		req("GET", mp, "", nil, 401)
		if !isAdmin {
			req("GET", mp+"?exp=1&sig=bad", "", nil, 403)
		}
		req("GET", files+"avatar/fl_nope00000000000000", "", key, 404)
		if isAdmin {
			req("GET", mp, "", ada, 403)
		}
		saved = f.reg.Realms["acme"].Settings.Storage
		broken = *saved
		broken.AccessKey = "NOT_SET_ANYWHERE"
		f.reg.Realms["acme"].Settings.Storage = &broken
		req("GET", mp, "", key, 503)
		req("POST", files+"avatar", png, typed(key, "image/png"), 503)
		f.reg.Realms["acme"].Settings.Storage = saved

		// Removing: one file, then the field.
		req("DELETE", files+"avatar/"+avatar, "", nil, 401)
		if isAdmin {
			req("DELETE", files+"avatar/"+avatar, "", ada, 403)
			req("DELETE", files+"receipts", "", ada, 403)
		}
		req("DELETE", files+"avatar/"+avatar, "", with(key, "If-Match", "bad"), 400)
		req("DELETE", files+"avatar/"+avatar, "", with(key, "If-Match", `"999"`), 412)
		req("DELETE", files+"avatar/"+avatar, "", key, 200)
		req("DELETE", files+"avatar/"+avatar, "", key, 404)
		req("DELETE", files+"receipts", "", nil, 401)
		req("DELETE", files+"receipts", "", with(key, "If-Match", "bad"), 400)
		req("DELETE", files+"receipts", "", with(key, "If-Match", `"999"`), 412)
		req("DELETE", base+"/nope/_files/receipts", "", key, 404)
		req("DELETE", files+"receipts", "", key, 200)

		if !isAdmin {
			// The rule that reserves a field refuses a client and accepts the key (above).
			mine := req("POST", base, `{"title": "again"}`, ada, 201)["id"].(string)
			req("DELETE", base+"/"+mine+"/_files/avatar", "", carl, 404)
			// A field the rules reserve stays: a function (the key) sets it, the owner can't take it away.
			req("POST", base+"/"+mine+"/_files/thumbnail", png, typed(key, "image/png"), 201)
			req("DELETE", base+"/"+mine+"/_files/thumbnail", "", ada, 403)
			th := req("GET", base+"/"+mine, "", ada, 200)["thumbnail"].(map[string]any)["id"].(string)
			req("DELETE", base+"/"+mine+"/_files/thumbnail/"+th, "", ada, 403)
			req("DELETE", base+"/"+mine+"/_files/avatar", "", with(ada, "If-Match", "bad"), 400)
			req("GET", base+"/"+mine+"?file_links=true", "", ada, 200)
			req("GET", base+"?file_links=true", "", ada, 200)
			// Conditional GET: the current tag revalidates, a stale one doesn't, and
			// a caller without read access still gets 404, never 304.
			cur := req("GET", base+"/"+mine, "", ada, 200)["_meta"].(map[string]any)["version"].(float64)
			tag := fmt.Sprintf(`"%d"`, int64(cur))
			req("GET", base+"/"+mine, "", with(ada, "If-None-Match", tag), 304)
			req("GET", base+"/"+mine, "", with(ada, "If-None-Match", `"0"`), 200)
			req("GET", base+"/"+mine, "", with(carl, "If-None-Match", tag), 404)
			req("GET", base+"/"+mine+"?file_links=true", "", with(ada, "If-None-Match", tag), 200)
			lrec, _ := f.doH(t, "GET", base, "", ada)
			req("GET", base, "", with(ada, "If-None-Match", lrec.Header().Get("ETag")), 304)
			req("GET", base+"?file_links=true", "", with(ada, "If-None-Match", lrec.Header().Get("ETag")), 200)
		} else {
			doc := req("POST", base, `{"title": "cond"}`, key, 201)
			tag := fmt.Sprintf(`"%d"`, int64(doc["_meta"].(map[string]any)["version"].(float64)))
			req("GET", base+"/"+doc["id"].(string), "", with(key, "If-None-Match", tag), 304)
			lrec, _ := f.doH(t, "GET", base, "", key)
			req("GET", base, "", with(key, "If-None-Match", lrec.Header().Get("ETag")), 304)
		}
	}
	// Direct uploads: start, PUT to the bucket as a browser would, complete.
	for _, base := range []string{"/v1/acme/app/videos", ad + "/data/app/videos"} {
		isAdmin := strings.Contains(base, "_admin")
		data := pngBytes(30)
		decl := func(d []byte, typ string) string { return declare("c.png", d, typ) }
		startAt := func(path string, d []byte, typ string, hdr map[string]string, want int) map[string]any {
			return req("POST", path, decl(d, typ), hdr, want)
		}
		done := func(field string, out map[string]any, token string, hdr map[string]string, want int) map[string]any {
			return req("POST", base+"/_files/"+field+"/uploads/"+out["upload_id"].(string)+"/complete", `{"upload_token": "`+token+`"}`, hdr, want)
		}
		// A pending one, and the document made with it.
		pst := startAt(base+"/_files/clip/uploads", data, "image/png", key, 201)
		putToLink(t, pst, data)
		done("clip", pst, pst["upload_token"].(string), key, 200)
		doc := req("POST", base, `{"title": "v", "clip": {"upload": "`+pst["upload_id"].(string)+`", "token": "`+pst["upload_token"].(string)+`"}}`, key, 201)["id"].(string)
		dpath := base + "/" + doc + "/_files/clips/uploads"

		// Starting.
		startAt(dpath, data, "image/png", key, 201)
		req("POST", dpath, `{}`, key, 400)
		startAt(dpath, data, "image/png", nil, 401)
		if isAdmin {
			startAt(dpath, data, "image/png", ada, 403)
		} else {
			vids := f.reg.Realms["acme"].Databases["app"].Collections["videos"]
			rl := vids.Rules
			vids.Rules = nil
			startAt(dpath, data, "image/png", ada, 403)
			vids.Rules = rl
		}
		startAt(base+"/nope/_files/clips/uploads", data, "image/png", key, 404)
		startAt(base+"/"+doc+"/_files/photo/uploads", data, "image/png", key, 409)
		startAt(base+"/"+doc+"/_files/clip/uploads", data, "text/plain", key, 415)
		startAt(dpath, data, "image/png", with(key, "If-Match", `"99"`), 412)
		saved := f.reg.Realms["acme"].Settings.Storage
		broken := *saved
		broken.AccessKey = "NOT_SET_ANYWHERE"
		f.reg.Realms["acme"].Settings.Storage = &broken
		startAt(dpath, data, "image/png", key, 503)
		f.reg.Realms["acme"].Settings.Storage = saved

		// Completing.
		ok := startAt(dpath, data, "image/png", key, 201)
		tok := ok["upload_token"].(string)
		done("clips", ok, tok, key, 409) // nothing uploaded yet
		done("clips", ok, "wrong", key, 400)
		done("clips", ok, tok, nil, 401)
		if isAdmin {
			done("clips", ok, tok, ada, 403)
		} else {
			vids := f.reg.Realms["acme"].Databases["app"].Collections["videos"]
			rl := vids.Rules
			vids.Rules = nil
			done("clips", ok, tok, ada, 403)
			vids.Rules = rl
		}
		req("POST", base+"/_files/nope/uploads/"+ok["upload_id"].(string)+"/complete", `{"upload_token": "`+tok+`"}`, key, 404)
		putToLink(t, ok, data)
		done("clips", ok, tok, with(key, "If-Match", `"99"`), 412)
		// That attempt attached nothing and abandoned the upload; a new one is completed.
		ok = startAt(dpath, data, "image/png", key, 201)
		putToLink(t, ok, data)
		done("clips", ok, ok["upload_token"].(string), key, 201)
		// A file that isn't what was declared.
		text := []byte(strings.Repeat("plain words ", 8))
		bad := startAt(base+"/"+doc+"/_files/clip/uploads", text, "image/png", key, 201)
		putToLink(t, bad, text)
		done("clip", bad, bad["upload_token"].(string), key, 415)
		wrong := startAt(dpath, data, "image/png", key, 201)
		fake.Put("contract/acme/app/videos/"+wrong["upload_id"].(string), pngBytes(31))
		done("clips", wrong, wrong["upload_token"].(string), key, 422)
		last := startAt(dpath, data, "image/png", key, 201)
		f.reg.Realms["acme"].Settings.Storage = &broken
		done("clips", last, last["upload_token"].(string), key, 503)
		f.reg.Realms["acme"].Settings.Storage = saved
	}

	startAtBase := func(base string, who map[string]string, field string) string { // a video document of the caller's
		d := pngBytes(7)
		jb := with(who, "Content-Type", "application/json")
		st := req("POST", base+"/videos/_files/"+field+"/uploads", declare("c.png", d, "image/png"), jb, 201)
		putToLink(t, st, d)
		req("POST", base+"/videos/_files/"+field+"/uploads/"+st["upload_id"].(string)+"/complete", `{"upload_token": "`+st["upload_token"].(string)+`"}`, jb, 200)
		return req("POST", base+"/videos", `{"title": "mine", "clip": {"upload": "`+st["upload_id"].(string)+`", "token": "`+st["upload_token"].(string)+`"}}`, who, 201)["id"].(string)
	}
	// A caller holds at most 20 unused uploads, whichever way they start: the three
	// routes that start one answer 429 past it.
	for _, base := range []string{"/v1/acme/app", ad + "/data/app"} {
		who := carlOr(key, strings.Contains(base, "_admin"), carl)
		jsonBody := with(who, "Content-Type", "application/json")
		mine := startAtBase(base, who, "clip")
		for range 25 {
			f.doH(t, "POST", base+"/videos/_files/clip/uploads", declare("c.png", pngBytes(3), "image/png"), jsonBody)
		}
		req("POST", base+"/library/_files/avatar/uploads", png, typed(who, "image/png"), 429)
		req("POST", base+"/videos/_files/clip/uploads", declare("c.png", pngBytes(3), "image/png"), jsonBody, 429)
		req("POST", base+"/videos/"+mine+"/_files/clips/uploads", declare("c.png", pngBytes(3), "image/png"), jsonBody, 429)
	}
	// A change to the configuration, checked and not applied.
	cc := ad + "/config/check"
	req("POST", cc, `{"files": {}}`, key, 200)
	req("POST", cc, `{"files": {"app/posts/collection.yaml": "rules:\n  read: document.nothing == 1\n"}}`, key, 200) // ok: false
	req("POST", cc, `{"files": {"../x": "a"}}`, key, 400)
	req("POST", cc, `{"files": {}}`, nil, 401)
	req("POST", cc, `{"files": {}}`, ada, 403)
	// The storage's status, and reconcile.
	req("GET", ad+"/storage", "", key, 200)
	req("GET", ad+"/storage", "", nil, 401)
	req("GET", ad+"/storage", "", ada, 403)
	// Public consent texts.
	{
		list := req("GET", "/v1/acme/_consents", "", nil, 200)
		items := list["items"].([]any)
		first := items[0].(map[string]any)
		if len(items) != 1 || first["name"] != "marketing" || first["required"] != false || first["current"].(map[string]any)["version"] != "1" {
			t.Errorf("consents = %v", list)
		}
		text := req("GET", "/v1/acme/_consents/marketing/1?locale=es-MX", "", nil, 200)
		if text["locale"] != "es" || text["text"] != "Enviadme novedades por correo." || len(text["hash"].(string)) != 64 || text["current"] != true {
			t.Errorf("text = %v", text)
		}
		req("GET", "/v1/acme/_consents/marketing/9", "", nil, 404)
		req("GET", "/v1/acme/_consents/nope/1", "", nil, 404)
		req("GET", "/v1/nope/_consents", "", nil, 404)
	}
	// Accepting consents: an optional one with a session, and a required one a sign-in waits for.
	{
		req("POST", "/v1/acme/_auth/consents", `{"consents": {"marketing": "1"}}`, nil, 401)
		req("POST", "/v1/acme/_auth/consents", `{"consents": {}}`, ada, 400)
		req("POST", "/v1/acme/_auth/consents", `{"consents": {"marketing": "1"}}`, ada, 200)
		saved := f.svc.Settings.Consents
		withTerms := map[string]*registry.Consent{}
		for k, v := range saved {
			withTerms[k] = v
		}
		withTerms["terms"] = &registry.Consent{
			Name: "terms", Required: true, Current: "1",
			Versions: map[string]*registry.ConsentVersion{"1": {}},
			Texts:    map[string]map[string]registry.ConsentText{"1": {"en": {Body: "terms", Hash: strings.Repeat("b", 64)}}},
		}
		f.svc.Settings.Consents = withTerms
		ch := req("POST", "/v1/acme/_auth/login", `{"email": "bob@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)
		if ch["consent_required"] != true {
			t.Fatalf("login asks no consent: %v", ch)
		}
		req("POST", "/v1/acme/_auth/consents/accept", `{"challenge": "`+ch["challenge"].(string)+`", "consents": {}}`, nil, 400)
		req("POST", "/v1/acme/_auth/consents/accept", `{"challenge": "`+ch["challenge"].(string)+`", "consents": {"terms": "1"}}`, nil, 200)
		f.svc.Settings.Consents = saved
	}

	// A user's own consents: read, withdraw, and what can't be withdrawn.
	{
		req("GET", "/v1/acme/_auth/consents", "", nil, 401)
		mine := req("GET", "/v1/acme/_auth/consents", "", ada, 200)
		if mine["consents"].(map[string]any)["marketing"] != "1" {
			t.Errorf("consents = %v", mine)
		}
		req("DELETE", "/v1/acme/_auth/consents/marketing", "", nil, 401)
		req("DELETE", "/v1/acme/_auth/consents/nope", "", ada, 404)
		req("DELETE", "/v1/acme/_auth/consents/marketing", "", ada, 200)
		req("DELETE", "/v1/acme/_auth/consents/marketing", "", ada, 200) // idempotent
		saved := f.svc.Settings.Consents
		required := map[string]*registry.Consent{"marketing": {Name: "marketing", Required: true, Current: "1",
			Versions: saved["marketing"].Versions, Texts: saved["marketing"].Texts}}
		f.svc.Settings.Consents = required
		req("DELETE", "/v1/acme/_auth/consents/marketing", "", ada, 409)
		f.svc.Settings.Consents = saved
	}
	// An administrator records a consent given on paper, and reads a user's history.
	{
		path := ad + "/users/" + f.adaID + "/consents"
		req("GET", path, "", nil, 401)
		req("GET", path, "", ada, 403)
		req("GET", ad+"/users/nobody/consents", "", key, 404)
		req("POST", path, `{"consent": "marketing", "version": "1", "action": "accepted", "evidence": "form 17"}`, nil, 401)
		req("POST", path, `{"consent": "marketing", "version": "1", "action": "accepted", "evidence": "form 17"}`, ada, 403)
		req("POST", ad+"/users/nobody/consents", `{"consent": "marketing", "version": "1", "action": "accepted", "evidence": "x"}`, key, 404)
		req("POST", path, `{"consent": "marketing", "version": "1", "action": "accepted", "evidence": ""}`, key, 400)
		rec := req("POST", path, `{"consent": "marketing", "version": "1", "action": "accepted", "evidence": "form 17"}`, key, 201)
		if rec["method"] != "admin" || rec["evidence"] != "form 17" {
			t.Errorf("record = %v", rec)
		}
		hist := req("GET", path, "", key, 200)
		if hist["consents"].(map[string]any)["marketing"] != "1" || len(hist["history"].([]any)) == 0 {
			t.Errorf("history = %v", hist)
		}
	}
	// Asking users to review a consent: a run, a bad name, and the right to do it.
	req("POST", ad+"/consents/notify", `{"consent": "marketing"}`, nil, 401)
	req("POST", ad+"/consents/notify", `{"consent": "marketing"}`, ada, 403)
	req("POST", ad+"/consents/notify", `{"consent": "nope"}`, key, 400)
	notified := req("POST", ad+"/consents/notify", `{"consent": "marketing"}`, key, 200)
	if notified["version"] != "1" || notified["queued"] != float64(0) {
		t.Errorf("notify = %v", notified)
	}
	{
		saved := f.svc.Settings.Email
		f.svc.Settings.Email = nil
		req("POST", ad+"/consents/notify", `{"consent": "marketing"}`, key, 409)
		f.svc.Settings.Email = saved
	}
	// The personal data scope: what an export would take from each collection.
	req("GET", ad+"/compliance/scope", "", nil, 401)
	req("GET", ad+"/compliance/scope", "", ada, 403)
	scope := req("GET", ad+"/compliance/scope", "", key, 200)
	excluded := false
	for _, c := range scope["collections"].([]any) {
		c := c.(map[string]any)
		if c["collection"] == "private" {
			excluded = c["export"] == "exclude" && c["reason"] == "fraud scores, exempt from access requests"
		}
	}
	if !excluded {
		t.Errorf("scope doesn't list the excluded collection: %v", scope["collections"])
	}
	// Reading the app configuration, anonymously and signed in, with revalidation.
	req("GET", "/v1/acme/_config", "", nil, 200)
	req("GET", "/v1/acme/_config?paths=maintenance", "", ada, 200)
	req("GET", "/v1/acme/_config?paths=nope", "", nil, 400)
	req("GET", "/v1/acme/_config", "", with(nil, "Authorization", "Bearer nope"), 401)
	req("GET", "/v1/nope/_config", "", nil, 404)

	// Realtime: a stream is a response that doesn't end, so the recorder (which can't flush)
	// ends it after `ready`; what is checked is the status and the headers.
	liveSub := `{"subscriptions":[{"id":"a","db":"app","collection":"live"}]}`
	req("POST", "/v1/acme/_realtime", liveSub, ada, 200)
	req("POST", "/v1/acme/_realtime", liveSub, ada, 429)
	req("POST", "/v1/acme/_realtime", `{"subscriptions":[]}`, carl, 400)
	req("POST", "/v1/acme/_realtime", liveSub, nil, 401)
	req("POST", "/v1/acme/_realtime", `{"subscriptions":[{"id":"a","db":"app","collection":"private"}]}`, carl, 403)
	cfgTag := ""
	{
		rec, _ := f.doH(t, "GET", "/v1/acme/_config", "", nil)
		cfgTag = rec.Header().Get("ETag")
	}
	req("GET", "/v1/acme/_config", "", with(nil, "If-None-Match", cfgTag), 304)
	// App configuration: read, change with If-Match, history and rollback.
	cfg := ad + "/app-config"
	req("GET", cfg, "", nil, 401)
	req("GET", cfg, "", ada, 403)
	req("GET", cfg, "", key, 200)
	jkey := typed(key, "application/json")
	req("PATCH", cfg, `{"set": {"maintenance.enabled": true}}`, jkey, 428)
	req("PATCH", cfg, `{"set": {"maintenance.enabled": true}}`, with(jkey, "If-Match", "bad"), 400)
	req("PATCH", cfg, `{"set": {"maintenance.enabled": "yes"}}`, with(jkey, "If-Match", `"c0"`), 400)
	req("PATCH", cfg, `{"set": {"maintenance.enabled": true}}`, with(jkey, "If-Match", `"c9"`), 412)
	req("PATCH", cfg, `{"set": {"maintenance.enabled": true}}`, with(jkey, "If-Match", `"c0"`), 200)
	req("PATCH", cfg, `{"set": {"maintenance.enabled": true}}`, with(jkey, "If-Match", `"c1"`), 200)
	req("PATCH", cfg, `{"reset": ["maintenance"]}`, with(jkey, "If-Match", `"c1"`), 200)
	req("GET", cfg+"/history", "", key, 200)
	req("GET", cfg+"/history", "", nil, 401)
	req("GET", cfg+"/history", "", ada, 403)
	req("GET", "/v1/nope/_admin/app-config", "", key, 404)
	req("GET", "/v1/nope/_admin/app-config/history", "", key, 404)
	req("PATCH", "/v1/nope/_admin/app-config", `{"reset": ["maintenance"]}`, with(jkey, "If-Match", `"c0"`), 404)
	req("PATCH", cfg, `{"reset": ["maintenance"]}`, typed(nil, "application/json"), 401)
	req("PATCH", cfg, `{"reset": ["maintenance"]}`, typed(ada, "application/json"), 403)
	req("GET", cfg+"/history?limit=x", "", key, 400)
	req("POST", cfg+"/rollback", `{"version": "c1"}`, with(jkey, "If-Match", `"c2"`), 200)
	req("POST", cfg+"/rollback", `{"version": "c40"}`, with(jkey, "If-Match", `"c3"`), 404)
	req("POST", cfg+"/rollback", `{"version": "c1"}`, with(jkey, "If-Match", `"c9"`), 412)
	req("POST", cfg+"/rollback", `{"version": "c1"}`, jkey, 428)
	req("POST", cfg+"/rollback", `{"version": "x"}`, with(jkey, "If-Match", `"c3"`), 400)
	req("POST", cfg+"/rollback", `{"version": "c1"}`, typed(nil, "application/json"), 401)
	req("POST", cfg+"/rollback", `{"version": "c1"}`, typed(ada, "application/json"), 403)
	req("POST", ad+"/storage/reconcile", `{}`, key, 200)
	req("POST", ad+"/storage/reconcile", `{"delete": true}`, key, 200)
	req("POST", ad+"/storage/reconcile", `{"delete": 5}`, key, 400)
	req("POST", ad+"/storage/reconcile", `{}`, nil, 401)
	req("POST", ad+"/storage/reconcile", `{}`, ada, 403)
	// Making stale image versions again.
	rg := ad + "/files/versions/regenerate"
	req("POST", rg, `{"database": "app", "collection": "library", "field": "picture"}`, nil, 401)
	req("POST", rg, `{"database": "app", "collection": "library", "field": "picture"}`, ada, 403)
	req("POST", rg, `{}`, key, 400)
	req("POST", rg, `{"database": "app", "collection": "library", "field": "nope"}`, key, 404)
	req("POST", rg, `{"database": "app", "collection": "library", "field": "picture", "version": "thumb", "missing_only": true, "rate": 5}`, key, 202)
	req("POST", rg, `{"database": "app", "collection": "library", "field": "picture"}`, key, 409)
	saved0 := f.reg.Realms["acme"].Settings.Storage
	broken0 := *saved0
	broken0.AccessKey = "NOT_SET_ANYWHERE"
	f.reg.Realms["acme"].Settings.Storage = &broken0
	req("POST", ad+"/storage/reconcile", `{}`, key, 503)
	f.reg.Realms["acme"].Settings.Storage = nil
	req("POST", ad+"/storage/reconcile", `{}`, key, 404)
	req("GET", ad+"/storage", "", key, 200) // not configured
	f.reg.Realms["acme"].Settings.Storage = saved0

	// Schema checks.
	checks := ad + "/data-checks"
	req("GET", checks, "", key, 200)
	req("GET", checks, "", nil, 401)
	req("GET", checks, "", ada, 403)
	req("POST", checks, `{"database": "app", "collection": "ghost"}`, key, 404)
	req("POST", checks, `{"database": "app", "limit": 0}`, key, 400)
	req("POST", checks, `{"database": "app"}`, nil, 401)
	req("POST", checks, `{"database": "app"}`, ada, 403)
	checkID := req("POST", checks, `{"database": "app", "collection": "notes", "limit": 20}`, key, 202)["id"].(string)
	req("POST", checks, ``, key, 409) // one at a time
	req("GET", checks, "", key, 200)  // now with a check running
	req("POST", ad+"/jobs/"+checkID+"/cancel", ``, key, 200)
	req("GET", checks+"/app/notes", "", key, 404) // never checked
	if err := f.svc.SaveCheckReport(context.Background(), auth.CheckReport{Database: "app", Collection: "notes", JobID: checkID, StartedAt: *f.clock, FinishedAt: *f.clock, Scanned: 3, Invalid: 1, Complete: true, Limit: 20, SchemaHash: "abc",
		Documents: []auth.CheckedDocument{{ID: "n1", Deleted: true, Problems: []auth.CheckProblem{{Path: "title", Reason: "is required"}}}}}); err != nil {
		t.Fatal(err)
	}
	req("GET", checks+"/app/notes", "", key, 200)
	req("GET", checks, "", key, 200) // with a report
	req("GET", checks+"/app/notes", "", nil, 401)
	req("GET", checks+"/app/notes", "", ada, 403)
	// Cancelling and re-running jobs.
	jobs := ad + "/jobs/"
	req("GET", jobs+jobID, "", key, 200)
	req("GET", jobs+"nope", "", key, 404)
	req("GET", jobs+jobID, "", nil, 401)
	req("GET", jobs+jobID, "", ada, 403)
	req("POST", jobs+jobID+"/rerun", ``, key, 409) // still queued
	req("POST", jobs+jobID+"/cancel", ``, key, 200)
	req("POST", jobs+jobID+"/cancel", ``, key, 409) // already finished
	req("POST", jobs+"nope/cancel", ``, key, 404)
	req("POST", jobs+"nope/rerun", ``, key, 404)
	req("POST", jobs+jobID+"/cancel", ``, nil, 401)
	req("POST", jobs+jobID+"/rerun", ``, nil, 401)
	req("POST", jobs+jobID+"/cancel", ``, ada, 403)
	req("POST", jobs+jobID+"/rerun", ``, ada, 403)
	req("POST", jobs+jobID+"/rerun", ``, key, 202)
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
	erase := req("DELETE", ad+"/users/"+dan, "", key, 202)
	req("DELETE", ad+"/users/"+dan, "", key, 202) // the job is still going: the same answer
	if err := f.svc.CompleteJob(context.Background(), erase["id"].(string), auth.JobResult{Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	req("DELETE", ad+"/users/"+dan, "", key, 409) // erased, nothing left to do
	req("DELETE", ad+"/users/nope", "", key, 404)

	// Documents. "posts" has rules, "private" has none (API keys only).
	p, priv := posts, "/v1/acme/app/private"
	doc := req("POST", p, `{"title": "hello", "published": true}`, ada, 201)
	id := p + "/" + doc["id"].(string)
	req("POST", p, `{"published": true}`, ada, 400)
	req("POST", p, `{"title": "x"}`, nil, 401)
	req("POST", p, `{"title": "x"}`, carl, 403)
	// An idempotent create, and its replay.
	keyed := with(ada, "Idempotency-Key", "contract-key")
	req("POST", p, `{"title": "keyed"}`, keyed, 201)
	req("POST", p, `{"title": "keyed"}`, keyed, 201)
	req("POST", p, `{"title": "keyed, another"}`, keyed, 422)
	readOnly := f.scoped(t, "contract-ro", "read:app/posts")
	req("GET", p, "", bearer(readOnly), 200)
	req("POST", p, `{"title": "x"}`, bearer(readOnly), 403) // outside the key's scopes
	req("POST", "/v1/acme/app/nope", `{"title": "x"}`, key, 404)
	req("POST", p, `{"title": "x"}`, with(key, "Content-Type", "text/plain"), 415)
	f.store.fail = errConflictForContract
	req("POST", p, `{"title": "x"}`, key, 409)
	f.store.fail = nil

	req("GET", p+"?count=true", "", nil, 200)
	req("GET", p+"?where={bad", "", nil, 400)
	req("POST", p, `{"title": "second", "published": true}`, ada, 201)
	if first := req("GET", p+"?limit=1&order_by=title", "", nil, 200); first["next_cursor"] == nil || first["has_more"] != true {
		t.Errorf("a first page has a next_cursor: %v", first)
	} else {
		req("GET", p+"?limit=1&order_by=title&after="+first["next_cursor"].(string), "", nil, 200)
	}
	req("GET", p+"?after=x&skip=1", "", nil, 400)
	req("GET", p+"?order_by=title&after=nope", "", nil, 400)
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
	batchKey := with(ada, "Idempotency-Key", "contract-batch")
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "kb"}}]}`, batchKey, 200)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "kb"}}]}`, batchKey, 200)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "other"}}]}`, batchKey, 422)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts"}]}`, ada, 400)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, nil, 401)
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, carl, 403)
	req("POST", b, `{"operations": [{"op": "create", "collection": "nope", "document": {}}]}`, key, 404)
	f.store.fail = errConflictForContract
	req("POST", b, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, key, 409)
	f.store.fail = nil
	req("POST", b, `{"operations": [{"op": "patch", "collection": "posts", "id": "`+batchID+`", "patch": {"title": "y"}, "if_match": "\"999\""}]}`, ada, 412)

	// The admin data route: the same operations, past the rules, for an admin.
	{
		const adm = "/v1/acme/_admin/data/app"
		ap := adm + "/posts"
		adoc := req("POST", ap, `{"title": "hello", "published": true}`, key, 201)
		aid := ap + "/" + adoc["id"].(string)
		req("POST", ap, `{"published": true}`, key, 400)
		req("POST", ap, `{"title": "x"}`, nil, 401)
		req("POST", ap, `{"title": "x"}`, ada, 403)
		req("POST", adm+"/nope", `{"title": "x"}`, key, 404)
		req("POST", ap, `{"title": "x"}`, with(key, "Content-Type", "text/plain"), 415)
		akeyed := with(key, "Idempotency-Key", "contract-admin-key")
		req("POST", ap, `{"title": "keyed"}`, akeyed, 201)
		req("POST", ap, `{"title": "keyed"}`, akeyed, 201)
		req("POST", ap, `{"title": "keyed, another"}`, akeyed, 422)
		f.store.fail = errConflictForContract
		req("POST", ap, `{"title": "x"}`, key, 409)
		f.store.fail = nil

		req("GET", ap+"?count=true", "", key, 200)
		req("GET", ap+"?where={bad", "", key, 400)
		req("GET", ap, "", nil, 401)
		req("GET", ap, "", ada, 403)
		req("GET", adm+"/nope", "", key, 404)
		req("GET", aid, "", key, 200)
		req("GET", aid, "", nil, 401)
		req("GET", aid, "", ada, 403)
		req("GET", ap+"/nope", "", key, 404)

		for _, m := range []string{"PUT", "PATCH"} {
			body := `{"title": "hello", "published": true}`
			req(m, aid, body, key, 200)
			req(m, aid, `{"title": 5}`, key, 400)
			req(m, aid, body, nil, 401)
			req(m, aid, body, ada, 403)
			req(m, ap+"/nope", body, key, 404)
			req(m, aid, body, with(key, "If-Match", `"999"`), 412)
			f.store.fail = errConflictForContract
			req(m, aid, body, key, 409)
			f.store.fail = nil
		}
		req("PATCH", aid, `{"title": "x"}`, with(key, "Content-Type", "application/json"), 415)
		req("DELETE", aid, "", with(key, "If-Match", "3"), 400)
		req("DELETE", aid, "", nil, 401)
		req("DELETE", aid, "", ada, 403)
		req("DELETE", aid, "", with(key, "If-Match", `"999"`), 412)
		req("DELETE", aid, "", key, 204)
		req("DELETE", aid, "", key, 404)

		// The trash of a collection that soft-deletes, and restore.
		bid := req("POST", adm+"/bin", `{"title": "t"}`, key, 201)["id"].(string)
		req("DELETE", adm+"/bin/"+bid, "", key, 204)
		req("GET", adm+"/bin?deleted=only", "", key, 200)
		req("POST", adm+"/bin/nope/restore", "", key, 404)
		req("POST", adm+"/bin/"+bid+"/restore", "", nil, 401)
		req("POST", adm+"/bin/"+bid+"/restore", "", ada, 403)
		req("POST", adm+"/bin/"+bid+"/restore", "", with(key, "If-Match", "bad"), 400)
		req("POST", adm+"/bin/"+bid+"/restore", "", with(key, "If-Match", `"99"`), 412)
		req("POST", adm+"/bin/"+bid+"/restore", "", key, 200)

		ab := adm + "/_batch"
		abOK := req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "batch"}}]}`, key, 200)
		abID := abOK["results"].([]any)[0].(map[string]any)["id"].(string)
		abKey := with(key, "Idempotency-Key", "contract-admin-batch")
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "kb"}}]}`, abKey, 200)
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "other"}}]}`, abKey, 422)
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts"}]}`, key, 400)
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, nil, 401)
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, ada, 403)
		req("POST", ab, `{"operations": [{"op": "create", "collection": "nope", "document": {}}]}`, key, 404)
		f.store.fail = errConflictForContract
		req("POST", ab, `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "x"}}]}`, key, 409)
		f.store.fail = nil
		req("POST", ab, `{"operations": [{"op": "patch", "collection": "posts", "id": "`+abID+`", "patch": {"title": "y"}, "if_match": "\"999\""}]}`, key, 412)
	}

	// The assignment of organizations to a collection that had none is queued, and a second one
	// is refused while the first has not finished. No worker is running yet, so the first job
	// stays queued: with a worker it could be done before the second request arrives.
	{
		orgID := req("POST", "/v1/acme/_orgs", `{"name": "Early"}`, ada, 201)["id"].(string)
		const assign = "/v1/acme/_admin/data/app/shared/_assign_org"
		req("POST", assign, `{"org": "`+orgID+`"}`, key, 202)
		req("POST", assign, `{"org": "`+orgID+`"}`, key, 409) // one at a time per collection
	}

	// A worker runs from here on, for what waits on one: making versions of images.
	runWorker(t, newTestWorker(t, f))
	for _, base := range []string{"/v1/acme/app/library", ad + "/data/app/library"} {
		isAdmin := strings.Contains(base, "_admin")
		files := base + "/"
		_ = files

		// Versions of an image: made by a worker, which a function (or anything allowed to
		// update the document) can ask to make one again, with other parameters, or drop.
		pic := req("POST", base, `{"title": "picture"}`, key, 201)["id"].(string)
		picFile := req("POST", base+"/"+pic+"/_files/picture?name=p.png", string(realPNG(t, 20, 10)), typed(key, "image/png"), 201)["picture"].(map[string]any)["id"].(string)
		vp := base + "/" + pic + "/_files/picture/" + picFile + "/versions/"
		req("POST", vp+"thumb", "", key, 200)
		req("POST", vp+"big", `{"max_width": 5, "format": "png"}`, key, 200)
		req("POST", vp+"thumb", `{"max_width": 5}`, key, 403) // not writable
		req("POST", vp+"big", `{"max_width": 5, "fit": "zoom"}`, key, 400)
		req("POST", vp+"mark", "", key, 400) // nothing to remake it from
		req("POST", vp+"thumb", "", nil, 401)
		req("POST", vp+"nope", "", key, 404)
		req("POST", base+"/"+pic+"/_files/picture/fl_nope/versions/thumb", "", key, 404)
		if isAdmin {
			req("POST", vp+"thumb", "", ada, 403)
			req("DELETE", vp+"thumb", "", ada, 403)
		} else {
			lib := f.reg.Realms["acme"].Databases["app"].Collections["library"]
			rl := lib.Rules
			lib.Rules = nil
			req("POST", vp+"thumb", "", ada, 403)
			req("DELETE", vp+"thumb", "", ada, 403)
			lib.Rules = rl
		}
		noKeys := *f.reg.Realms["acme"].Settings.Storage
		noKeys.AccessKey = "NOT_SET_ANYWHERE"
		working := f.reg.Realms["acme"].Settings.Storage
		f.reg.Realms["acme"].Settings.Storage = &noKeys
		req("POST", vp+"thumb", "", key, 503)
		f.reg.Realms["acme"].Settings.Storage = working
		cut := realPNG(t, 20, 10)
		cutDoc := req("POST", base, `{"title": "cut"}`, key, 201)["id"].(string)
		cutFile := req("POST", base+"/"+cutDoc+"/_files/picture?name=cut.png", string(cut[:len(cut)-30]), typed(key, "image/png"), 201)["picture"].(map[string]any)["id"].(string)
		req("POST", base+"/"+cutDoc+"/_files/picture/"+cutFile+"/versions/thumb", "", key, 422)
		req("DELETE", vp+"mark", "", nil, 401)
		req("DELETE", vp+"nope", "", key, 404)
		req("DELETE", vp+"mark", "", key, 200)
		req("DELETE", vp+"thumb", "", key, 200)

	}

	// A proof is not endless: wrong passwords are throttled like logins, and a code is emailed
	// to a user who has no other proof, at most as often as the email limits allow.
	{
		ctx := context.Background()
		pw := "dev-p4ssw0rd!"
		if _, err := f.svc.Create(ctx, "hammer@example.com", &pw); err != nil {
			t.Fatal(err)
		}
		hammer := with(map[string]string{"Authorization": "Bearer " + req("POST", a+"/login", `{"email": "hammer@example.com", "password": "dev-p4ssw0rd!"}`, nil, 200)["token"].(string)}, "Content-Type", "application/json")
		for range 12 {
			if rec, _ := f.doH(t, "POST", a+"/reauth", `{"method": "password", "password": "wrong-wrong-wrong"}`, hammer); rec.Code == 429 {
				break
			}
		}
		req("POST", a+"/reauth", `{"method": "password", "password": "wrong-wrong-wrong"}`, hammer, 429)
		// Someone who signs in with a provider only has no password: a code by email.
		u, err := f.svc.Create(ctx, "providers-only@example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.svc.StartProviderSession(ctx, u, "google", false, nil)
		if err != nil {
			t.Fatal(err)
		}
		codeHdr := map[string]string{"Authorization": "Bearer " + out.Token}
		// (The address's hourly budget of emails went to the tests above.)
		limits := f.svc.Settings.Email.Limits
		f.svc.Settings.Email.Limits.PerIPPerHour = 1000
		req("POST", a+"/reauth/email-code", "", codeHdr, 202)
		f.svc.Settings.Email.Limits = limits
		for range 6 {
			if rec, _ := f.doH(t, "POST", a+"/reauth/email-code", "", codeHdr); rec.Code == 429 {
				break
			}
		}
		req("POST", a+"/reauth/email-code", "", codeHdr, 429)
	}

	// Magic links: a request, the page its link opens, the button, the code typed in the app.
	{
		ctx := context.Background()
		f.svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true, Signup: true}
		f.svc.Settings.SignInAppURL = "https://app.acme.example/signed-in"
		if f.svc.Cipher == nil {
			f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
		}
		magic, mform := a+"/magic-link", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
		verifier, challenge, _ := auth.NewPKCE()
		// The worker started above makes the secrets of a request when it writes the email, and a
		// second time would invalidate the first: wait until it is done with the request's job,
		// then make the ones the test knows.
		secrets := func(requestID string) (string, string) {
			for range 200 {
				pending := false
				jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Limit: 100})
				for _, j := range jobs {
					if j.Email != nil && j.Email.ChallengeID == auth.HashToken(requestID) && (j.Status == auth.JobQueued || j.Status == auth.JobRunning) {
						pending = true
					}
				}
				if !pending {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			tok, code, _, err := f.svc.MakeMagicLinkSecrets(ctx, auth.HashToken(requestID))
			if err != nil {
				t.Fatal(err)
			}
			return tok, code
		}
		// (Email limits are not what is under test here.)
		limits := f.svc.Settings.Email.Limits
		f.svc.Settings.Email.Limits = registry.EmailLimits{PerKindPerHour: 1000, PerDay: 1000, PerIPPerHour: 1000}
		ask := func(address, extra string) map[string]any {
			return req("POST", magic, `{"email": "`+address+`", "code_challenge": "`+challenge+`"`+extra+`}`, nil, 202)
		}
		first := ask("ada@example.com", `, "redirect_to": "https://app.acme.example/in"`)
		tok, code := secrets(first["request_id"].(string))
		req("GET", magic+"?token="+tok, "", nil, 200)
		req("GET", magic+"?token=nope", "", nil, 400)
		req("POST", magic, "token="+tok, mform, 303)
		req("POST", magic, "token="+tok, mform, 400)
		second := ask("ada@example.com", "")
		_, code2 := secrets(second["request_id"].(string))
		verify := magic + "/verify"
		body := func(id, c, v string) string {
			return `{"request_id": "` + id + `", "code": "` + c + `", "code_verifier": "` + v + `"}`
		}
		req("POST", verify, body(second["request_id"].(string), "000000", verifier), nil, 400)
		req("POST", verify, `{"request_id": "x"}`, nil, 400)
		req("POST", verify, body(second["request_id"].(string), code2, verifier), nil, 200)
		req("POST", verify, body(second["request_id"].(string), code2, verifier), nil, 400)
		_ = code
		// A newcomer in a realm with sign-up by link makes the account when the code is used.
		third := ask("magic-newcomer@example.com", "")
		_, code3 := secrets(third["request_id"].(string))
		if got := req("POST", verify, body(third["request_id"].(string), code3, verifier), nil, 200); got["new_user"] != true {
			t.Errorf("a newcomer by code: %v", got)
		}
		// A link with nowhere to go answers a page that says to type the code, and isn't used up.
		f.svc.Settings.SignInAppURL = ""
		lost := ask("ada@example.com", "")
		lostTok, _ := secrets(lost["request_id"].(string))
		req("POST", magic, "token="+lostTok, mform, 200)
		f.svc.Settings.SignInAppURL = "https://app.acme.example/signed-in"
		// An account that can't sign in is refused when the link is used.
		blocked, err := f.svc.Create(ctx, "magic-blocked@example.com", ptr("dev-p4ssw0rd!"))
		if err != nil {
			t.Fatal(err)
		}
		fourth := ask("magic-blocked@example.com", "")
		_, code4 := secrets(fourth["request_id"].(string))
		if err := f.svc.SetDisabled(ctx, blocked.Email, true); err != nil {
			t.Fatal(err)
		}
		req("POST", verify, body(fourth["request_id"].(string), code4, verifier), nil, 403)
		// The text of the page and the refusals of the route.
		req("POST", magic, `{"email": "ada@example.com", "code_challenge": "`+challenge+`", "redirect_to": "https://evil.example/"}`, nil, 400)
		req("POST", magic, `{"email": "ada@example.com"}`, map[string]string{"Content-Type": "text/plain"}, 415)
		req("POST", "/v1/nope/_auth/magic-link", `{"email": "a@example.com", "code_challenge": "`+challenge+`"}`, nil, 404)
		f.svc.Settings.MagicLink.Enabled = false
		req("POST", magic, `{"email": "ada@example.com", "code_challenge": "`+challenge+`"}`, nil, 403)
		req("GET", magic+"?token=x", "", nil, 404)
		req("POST", magic, "token=x", mform, 404)
		req("POST", verify, body("x", "1", "v"), nil, 403)
		f.svc.Settings.MagicLink.Enabled = true
		f.svc.Settings.Email.Limits = limits
		// Too many tries of the button, and of the code, from one address.
		for range 31 {
			f.doH(t, "POST", magic, "token=wrong", mform)
		}
		req("POST", magic, "token=wrong", mform, 429)
		for range 40 {
			f.doH(t, "POST", verify, body("nope", "000000", verifier), map[string]string{"Content-Type": "application/json"})
		}
		req("POST", verify, body("nope", "000000", verifier), nil, 429)
	}

	// Devices for push notifications: nothing without a push: section, then a registry.
	{
		dev := a + "/devices"
		req("POST", dev, `{"platform": "android", "token": "contract-token"}`, ada, 404)
		req("GET", dev, "", ada, 404)
		req("GET", dev+"/web-key", "", nil, 404)
		req("GET", ad+"/users/"+temp.ID+"/devices", "", key, 404)
		pushOff := req("GET", ad+"/push", "", key, 200)
		if pushOff["enabled"] != false {
			t.Errorf("push status without push: %v", pushOff)
		}
		req("GET", ad+"/push", "", nil, 401)
		req("GET", ad+"/push", "", ada, 403)
		req("GET", ad+"/push/sends", "", key, 404)
		req("DELETE", ad+"/push/devices?platform=web", "", key, 404)
		req("GET", ad+"/push/sends/ps_none", "", key, 404)
		req("POST", ad+"/push/test", `{"email": "ada@example.com", "title": "Hi", "body": "There"}`, key, 404)
		f.svc.Settings.Push = &registry.PushSettings{
			Platforms:         map[string]registry.PushPlatform{"android": {Provider: "fcm"}, "web": {Provider: "webpush"}},
			WebEndpointHosts:  registry.DefaultWebEndpointHosts,
			WebPublicKey:      "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U",
			MaxDevicesPerUser: 10,
			StaleAfter:        60 * 24 * time.Hour,
		}
		f.svc.Settings.Push.Limits = registry.PushLimits{PerMinute: 5, PerDay: 24}
		f.svc.Settings.Push.Delivery = registry.PushDelivery{Timeout: time.Minute, RetryAttempts: 3, RetryBackoff: 30 * time.Second, Parallelism: 4}
		f.svc.Settings.Push.SendRetention = 30 * 24 * time.Hour
		f.reg.Realms["acme"].Settings.Push = f.svc.Settings.Push
		if f.svc.Cipher == nil {
			f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
		}
		made := req("POST", dev, `{"platform": "android", "token": "contract-token", "name": "Contract phone"}`, ada, 200)
		req("POST", dev, `{"platform": "ios", "token": "contract-token"}`, ada, 400)
		req("POST", dev, `{"platform": "android", "token": "contract-token"}`, nil, 401)
		listed := req("GET", dev, "", ada, 200)
		if items, _ := listed["items"].([]any); len(items) != 1 {
			t.Errorf("devices: %v", listed)
		}
		req("GET", dev, "", nil, 401)
		req("GET", dev+"/web-key", "", nil, 200)
		req("DELETE", dev+"/nope", "", ada, 404)
		req("DELETE", dev+"/x", "", nil, 401)
		// An administrator sees the user's devices and removes one.
		req("GET", ad+"/users/"+temp.ID+"/devices", "", key, 200)
		req("GET", ad+"/users/"+temp.ID+"/devices", "", nil, 401)
		req("GET", ad+"/users/"+temp.ID+"/devices", "", ada, 403)
		req("DELETE", ad+"/users/"+temp.ID+"/devices/nope", "", key, 404)
		req("DELETE", ad+"/users/"+temp.ID+"/devices/x", "", nil, 401)
		req("DELETE", ad+"/users/"+temp.ID+"/devices/x", "", ada, 403)
		req("DELETE", dev+"/"+made["id"].(string), "", ada, 204)
		// Thirty registrations an hour per session.
		for range 30 {
			f.doH(t, "POST", dev, `{"platform": "android", "token": "contract-token-2"}`, map[string]string{"Authorization": ada["Authorization"], "Content-Type": "application/json"})
		}
		req("POST", dev, `{"platform": "android", "token": "contract-token-2"}`, ada, 429)
		// A device of the temporary user, removed by an administrator.
		tmp, _, err := f.svc.Login(context.Background(), temp.Email, "dev-p4ssw0rd!", "203.0.113.51")
		if err != nil {
			t.Fatal(err)
		}
		d, err := f.svc.RegisterDevice(context.Background(), tmp, auth.DeviceInput{Platform: "android", Token: "temp-token"})
		if err != nil {
			t.Fatal(err)
		}
		req("DELETE", ad+"/users/"+temp.ID+"/devices/"+d.ID, "", key, 204)

		// What the capture provider took: nothing while the realm has no capture platform.
		req("GET", "/v1/acme/_dev/push", "", nil, 404)
		f.svc.Settings.Push.Platforms["android"] = registry.PushPlatform{Provider: "capture"}
		req("GET", "/v1/acme/_dev/push?limit=5", "", nil, 200)
		f.svc.Settings.Push.Platforms["android"] = registry.PushPlatform{Provider: "fcm"}
		// The sends of the realm, and a test push to a user.
		sent := req("POST", ad+"/push/test", `{"email": "`+temp.Email+`", "title": "Hi", "body": "There", "data": {"a": "b"}}`, key, 202)
		req("POST", ad+"/push/test", `{"email": "`+temp.Email+`", "title": "Hi", "body": "`+strings.Repeat("x", 4300)+`"}`, key, 413)
		req("POST", ad+"/push/test", `{"email": "`+temp.Email+`", "title": "Hi"}`, key, 400)
		req("POST", ad+"/push/test", `{"email": "nobody@example.com", "title": "Hi", "body": "There"}`, key, 404)
		req("POST", ad+"/push/test", `{"email": "ada@example.com", "title": "Hi", "body": "There"}`, nil, 401)
		req("POST", ad+"/push/test", `{"email": "ada@example.com", "title": "Hi", "body": "There"}`, ada, 403)
		f.svc.Settings.Push.Platforms["android"] = registry.PushPlatform{Provider: "fcm", Credentials: "NOT_SET"}
		req("POST", ad+"/push/test", `{"email": "`+temp.Email+`", "title": "Hi", "body": "There"}`, key, 503)
		f.svc.Settings.Push.Platforms["android"] = registry.PushPlatform{Provider: "fcm"}
		status := req("GET", ad+"/push", "", key, 200)
		if status["enabled"] != true || len(status["platforms"].([]any)) != 2 {
			t.Errorf("push status: %v", status)
		}
		sends := req("GET", ad+"/push/sends?kind=raw&limit=5", "", key, 200)
		if items, _ := sends["items"].([]any); len(items) < 1 {
			t.Errorf("sends: %v", sends)
		}
		req("GET", ad+"/push/sends?nope=1", "", key, 400)
		req("DELETE", ad+"/push/devices", "", key, 400)
		req("DELETE", ad+"/push/devices?platform=windows", "", key, 400)
		req("DELETE", ad+"/push/devices?platform=web", "", nil, 401)
		req("DELETE", ad+"/push/devices?platform=web", "", ada, 403)
		req("DELETE", ad+"/push/devices?platform=web", "", key, 200)
		req("GET", ad+"/push/sends", "", nil, 401)
		req("GET", ad+"/push/sends", "", ada, 403)
		sendID := sent["send_id"].(string)
		req("GET", ad+"/push/sends/"+sendID, "", key, 200)
		req("GET", ad+"/push/sends/ps_nope", "", key, 404)
		req("GET", ad+"/push/sends/"+sendID, "", nil, 401)
		req("GET", ad+"/push/sends/"+sendID, "", ada, 403)
	}

	// Organizations.
	{
		orgs, adm := "/v1/acme/_orgs", "/v1/acme/_admin/orgs"
		bob := bearer(f.bob)
		withJSON := func(h map[string]string) map[string]string { return with(h, "Content-Type", "application/json") }
		req("POST", orgs, `{"name": "Contract Inc"}`, nil, 401)
		req("POST", orgs, `{"name": "Contract Inc"}`, carl, 403)
		req("POST", orgs, `{}`, ada, 400)
		req("POST", "/v1/nope/_orgs", `{"name": "x"}`, ada, 404)
		created := req("POST", orgs, `{"name": "Contract Inc", "slug": "contract-inc"}`, ada, 201)
		id := created["id"].(string)
		req("POST", orgs, `{"name": "Dup", "slug": "contract-inc"}`, bob, 409)
		other := req("POST", orgs, `{"name": "Bob's", "slug": "bobs-org"}`, bob, 201)
		req("GET", orgs, "", nil, 401)
		req("GET", orgs, "", ada, 200)
		req("GET", "/v1/nope/_orgs", "", ada, 404)
		req("GET", orgs+"/"+id, "", nil, 401)
		req("GET", orgs+"/"+id, "", bob, 403)
		req("GET", orgs+"/"+id, "", ada, 200)
		req("GET", "/v1/nope/_orgs/"+id, "", ada, 404)
		req("PATCH", orgs+"/"+id, `{"name": "Contract Incorporated"}`, withJSON(nil), 401)
		req("PATCH", orgs+"/"+id, `{"slug": "Not A Slug"}`, withJSON(ada), 400)
		req("PATCH", orgs+"/"+id, `{"name": "Nope"}`, withJSON(bob), 403)
		req("PATCH", orgs+"/"+id, `{"slug": "bobs-org"}`, withJSON(ada), 409)
		req("PATCH", orgs+"/"+id, `{"name": "Contract Incorporated"}`, withJSON(ada), 200)
		// Members: bob joins as an admin through the admin API, and the owner is the last manager.
		req("POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`", "roles": ["admin"]}`, nil, 401)
		req("POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`"}`, ada, 403)
		req("POST", adm+"/"+id+"/members", `{"roles": ["admin"]}`, key, 400)
		req("POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`", "roles": ["wizard"]}`, key, 400)
		req("POST", adm+"/"+id+"/members", `{"user": "nosuchuser"}`, key, 404)
		req("POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`", "roles": ["admin"]}`, key, 200)
		req("GET", orgs+"/"+id+"/members", "", nil, 401)
		req("GET", orgs+"/"+id+"/members", "", carl, 403)
		req("GET", orgs+"/"+id+"/members?limit=1", "", ada, 200)
		member := orgs + "/" + id + "/members/" + f.bobID
		req("PATCH", member, `{"roles": ["editor"]}`, withJSON(nil), 401)
		req("PATCH", member, `{"roles": ["editor"]}`, withJSON(carl), 403)
		req("PATCH", member, `{"roles": ["wizard"]}`, withJSON(ada), 400)
		req("PATCH", member, `{"roles": ["editor"]}`, withJSON(ada), 200)
		req("PATCH", orgs+"/"+id+"/members/"+f.adaID, `{"roles": ["editor"]}`, withJSON(ada), 409)
		req("DELETE", member, "", nil, 401)
		req("DELETE", member, "", carl, 403)
		req("DELETE", orgs+"/"+id+"/members/"+f.adaID, "", ada, 409)
		req("DELETE", member, "", ada, 204)
		req("POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`"}`, key, 200)
		req("POST", orgs+"/"+id+"/leave", "", bob, 204)
		req("POST", orgs+"/"+id+"/leave", "", nil, 401)
		req("POST", orgs+"/"+id+"/leave", "", bob, 403)
		req("POST", orgs+"/"+id+"/leave", "", ada, 409)
		// The administrator's side.
		req("GET", adm, "", nil, 401)
		req("GET", adm, "", ada, 403)
		req("GET", adm+"?status=active", "", key, 200)
		req("GET", adm+"?q=ac", "", key, 200)
		req("GET", "/v1/nope/_admin/orgs", "", key, 404)
		req("POST", adm, `{"name": "Staff made", "owner": "`+f.bobID+`"}`, nil, 401)
		req("POST", adm, `{"name": "Staff made", "owner": "`+f.bobID+`"}`, ada, 403)
		req("POST", adm, `{"name": "Staff made"}`, key, 400)
		req("POST", adm, `{"name": "Staff made", "owner": "nosuchuser"}`, key, 404)
		req("POST", adm, `{"name": "Staff made", "slug": "bobs-org", "owner": "`+f.bobID+`"}`, key, 409)
		made := req("POST", adm, `{"name": "Staff made", "owner": "`+f.bobID+`"}`, key, 201)
		madeID := made["id"].(string)
		req("GET", adm+"/"+id, "", nil, 401)
		req("GET", adm+"/"+id, "", ada, 403)
		req("GET", adm+"/nosuch", "", key, 404)
		req("GET", adm+"/"+id, "", key, 200)
		req("GET", adm+"/"+id+"/members", "", nil, 401)
		req("GET", adm+"/"+id+"/members", "", ada, 403)
		req("GET", adm+"/nosuch/members", "", key, 404)
		req("GET", adm+"/"+id+"/members", "", key, 200)
		req("PUT", adm+"/"+id+"/claims", `{"plan": "pro"}`, nil, 401)
		req("PUT", adm+"/"+id+"/claims", `{"plan": "pro"}`, ada, 403)
		req("PUT", adm+"/nosuch/claims", `{"plan": "pro"}`, key, 404)
		req("PUT", adm+"/"+id+"/claims", `{"x": "`+strings.Repeat("a", 1100)+`"}`, key, 400)
		req("PUT", adm+"/"+id+"/claims", `{"plan": "pro"}`, key, 200)
		mem := adm + "/" + madeID + "/members/" + f.bobID
		req("PATCH", mem, `{"roles": ["editor"]}`, withJSON(nil), 401)
		req("PATCH", mem, `{"roles": ["editor"]}`, withJSON(ada), 403)
		req("PATCH", adm+"/"+madeID+"/members/nosuchuser", `{"roles": ["editor"]}`, withJSON(key), 404)
		req("PATCH", mem, `{"roles": ["wizard"]}`, withJSON(key), 400)
		req("PATCH", mem, `{"roles": ["owner", "editor"]}`, withJSON(key), 200)
		req("PATCH", mem, `{"roles": ["editor"]}`, withJSON(key), 409)
		req("DELETE", mem, "", nil, 401)
		req("DELETE", mem, "", ada, 403)
		req("DELETE", adm+"/"+madeID+"/members/nosuchuser", "", key, 404)
		req("DELETE", mem, "", key, 409)
		req("POST", adm+"/"+madeID+"/owner", `{"user": "`+f.adaID+`"}`, nil, 401)
		req("POST", adm+"/"+madeID+"/owner", `{"user": "`+f.adaID+`"}`, ada, 403)
		req("POST", adm+"/"+madeID+"/owner", `{}`, key, 400)
		req("POST", adm+"/"+madeID+"/owner", `{"user": "nosuchuser"}`, key, 404)
		// A user at the limit of memberships can't be added to another organization.
		// Deleting an organization.
		doomed := req("POST", adm, `{"name": "Doomed", "owner": "`+f.adaID+`"}`, key, 201)["id"].(string)
		req("GET", adm+"/"+doomed+"/owned", "", nil, 401)
		req("GET", adm+"/"+doomed+"/owned", "", ada, 403)
		req("GET", adm+"/nosuch/owned", "", key, 404)
		req("GET", adm+"/"+doomed+"/owned", "", key, 200)
		req("DELETE", orgs+"/"+doomed, "", nil, 401)
		req("DELETE", orgs+"/"+doomed, "", bob, 403)
		req("DELETE", orgs+"/"+doomed, "", ada, 401) // a fresh proof is needed
		req("DELETE", orgs+"/"+doomed, "", proofOf(f.ada, "dev-p4ssw0rd!"), 204)
		req("DELETE", adm+"/"+doomed, "", nil, 401)
		req("DELETE", adm+"/"+doomed, "", ada, 403)
		req("DELETE", adm+"/nosuch", "", key, 404)
		req("DELETE", adm+"/"+doomed, "", key, 409) // deleted already
		adminGone := req("POST", adm, `{"name": "Admin deleted", "owner": "`+f.adaID+`"}`, key, 201)["id"].(string)
		req("DELETE", adm+"/"+adminGone, "", key, 204)
		req("POST", adm+"/"+doomed+"/reactivate", `{}`, nil, 401)
		req("POST", adm+"/"+doomed+"/reactivate", `{}`, ada, 403)
		req("POST", adm+"/nosuch/reactivate", `{}`, key, 404)
		req("POST", adm+"/"+doomed+"/reactivate", `{"slug": "Not A Slug"}`, key, 400)
		req("POST", adm+"/"+id+"/reactivate", `{}`, key, 409) // not deleted
		req("POST", adm+"/"+doomed+"/reactivate", `{}`, key, 200)
		// Moving documents: the admin data route.
		dataRoute := "/v1/acme/_admin/data/app/projects"
		moving := req("POST", dataRoute+"?org="+id, `{"title": "moves"}`, key, 201)["id"].(string)
		to := other["id"].(string)
		tr := dataRoute + "/" + moving + "/_transfer"
		req("POST", tr, `{"org": "`+to+`"}`, nil, 401)
		req("POST", tr, `{"org": "`+to+`"}`, ada, 403)
		req("POST", tr, `{}`, key, 400)
		req("POST", dataRoute+"/nosuch/_transfer", `{"org": "`+to+`"}`, key, 404)
		req("POST", tr, `{"org": "nosuch"}`, key, 404)
		req("POST", tr, `{"org": "`+to+`"}`, with(key, "If-Match", `"99"`), 412)
		gone := req("POST", adm, `{"name": "Gone", "owner": "`+f.adaID+`"}`, key, 201)["id"].(string)
		req("DELETE", adm+"/"+gone, "", key, 204)
		req("POST", tr, `{"org": "`+gone+`"}`, key, 409)
		req("POST", tr, `{"org": "`+to+`"}`, key, 200)
		// Teams of a collection: the document carries _meta.team, and the transfer names it.
		deptTeam := req("POST", adm+"/"+to+"/teams", `{"name": "Contract department"}`, key, 201)["id"].(string)
		reqsRoute := "/v1/acme/_admin/data/app/requests"
		req("POST", reqsRoute+"?org="+to, `{"title": "no team"}`, key, 400)
		req("POST", reqsRoute+"?org="+to+"&team=nosuch", `{"title": "x"}`, key, 404)
		teamDoc := req("POST", reqsRoute+"?org="+to+"&team="+deptTeam, `{"title": "in a team"}`, key, 201)["id"].(string)
		req("GET", reqsRoute+"?org="+to+"&team="+deptTeam, "", key, 200)
		ttr := reqsRoute + "/" + teamDoc + "/_transfer"
		req("POST", ttr, `{"org": "`+to+`", "team": null}`, key, 400)
		req("POST", ttr, `{"org": "`+to+`", "team": "nosuch"}`, key, 404)
		req("POST", ttr, `{"org": "`+to+`", "team": "`+deptTeam+`"}`, key, 200)
		assign := "/v1/acme/_admin/data/app/shared/_assign_org"
		req("POST", assign, `{"org": "`+to+`"}`, nil, 401)
		req("POST", assign, `{"org": "`+to+`"}`, ada, 403)
		req("POST", assign, `{}`, key, 400)
		req("POST", assign, `{"org": "nosuch"}`, key, 404)
		req("POST", assign+"?dry_run=true", `{"org": "`+to+`"}`, key, 200)
		// 202 and the 409 of "one at a time per collection" are produced before the worker
		// starts (see above): a running worker finishes the first job before the second request.
		// Owner templates.
		f.svc.Settings.Email.Customizable = []string{"reset-password"}
		tplLimits := f.svc.Settings.Email.Limits
		f.svc.Settings.Email.Limits.PerKindPerHour, f.svc.Settings.Email.Limits.PerDay, f.svc.Settings.Email.Limits.PerIPPerHour = 100000, 100000, 100000
		tplBase := orgs + "/" + id + "/templates"
		tpl := tplBase + "/reset-password/en"
		goodTpl := `{"subject": "Reset", "text": "{{Link}}", "html": "<a href=\"{{Link}}\">Reset</a>"}`
		req("GET", tplBase, "", nil, 401)
		req("GET", tplBase, "", carl, 403)
		req("GET", tplBase, "", ada, 200)
		req("GET", tpl, "", nil, 401)
		req("GET", tpl, "", carl, 403)
		req("GET", tpl, "", ada, 404)
		req("GET", tplBase+"/verify-email/en", "", ada, 400)
		req("PUT", tpl, goodTpl, withJSON(nil), 401)
		req("PUT", tpl, goodTpl, withJSON(carl), 403)
		req("PUT", tpl, `{"subject": "x", "text": "no link", "html": "x"}`, withJSON(ada), 400)
		req("PUT", tplBase+"/verify-email/en", goodTpl, withJSON(ada), 400)
		req("PUT", tpl, goodTpl, withJSON(ada), 200)
		req("GET", tpl, "", ada, 200)
		req("POST", tpl+"/preview", `{}`, withJSON(nil), 401)
		req("POST", tpl+"/preview", `{}`, withJSON(carl), 403)
		req("POST", tpl+"/preview", `{"subject": "only"}`, withJSON(ada), 400)
		req("POST", tpl+"/preview", `{}`, withJSON(ada), 200)
		req("DELETE", tpl, "", ada, 204)
		req("POST", tpl+"/preview", `{}`, withJSON(ada), 404) // nothing saved
		req("POST", tpl+"/test", `{}`, withJSON(ada), 404)
		req("PUT", tpl, goodTpl, withJSON(ada), 200)
		req("POST", tplBase+"/magic-link/en/preview", `{}`, withJSON(ada), 400)
		req("POST", tpl+"/test", `{}`, withJSON(nil), 401)
		req("POST", tpl+"/test", `{}`, withJSON(carl), 403)
		req("POST", tpl+"/test", `{"subject": "only"}`, withJSON(ada), 400)
		req("POST", tpl+"/test", `{}`, withJSON(ada), 202)
		for i := 1; i < auth.OrgTemplateTestsPerHour; i++ {
			req("POST", tpl+"/test", `{}`, withJSON(ada), 202)
		}
		req("POST", tpl+"/test", `{}`, withJSON(ada), 429)
		req("DELETE", tpl, "", nil, 401)
		req("DELETE", tpl, "", carl, 403)
		req("DELETE", tpl, "", ada, 204)
		req("DELETE", tpl, "", ada, 404)
		req("DELETE", tplBase+"/verify-email/en", "", ada, 400)
		f.svc.Settings.Email.Limits = tplLimits
		f.svc.Settings.Email.Customizable = nil
		// Teams.
		teamsPath := orgs + "/" + id + "/teams"
		req("GET", teamsPath, "", nil, 401)
		req("GET", teamsPath, "", carl, 403)
		req("GET", teamsPath, "", ada, 200)
		req("POST", teamsPath, `{"name": "Editors", "roles": ["editor"]}`, nil, 401)
		req("POST", teamsPath, `{"name": "Editors"}`, carl, 403)
		req("POST", teamsPath, `{}`, ada, 400)
		req("POST", "/v1/acme/_orgs/nosuch/teams", `{"name": "x"}`, ada, 403)
		team := req("POST", teamsPath, `{"name": "Editors", "roles": ["editor"]}`, ada, 201)
		teamID := team["id"].(string)
		req("POST", teamsPath, `{"name": "Editors"}`, ada, 409)
		tp := teamsPath + "/" + teamID
		req("PATCH", tp, `{"name": "Writers"}`, withJSON(nil), 401)
		req("PATCH", tp, `{"name": "Writers"}`, withJSON(carl), 403)
		req("PATCH", tp, `{"roles": ["wizard"]}`, withJSON(ada), 400)
		req("PATCH", teamsPath+"/nosuch", `{"name": "x"}`, withJSON(ada), 404)
		req("PATCH", tp, `{"name": "Writers"}`, withJSON(ada), 200)
		tm := tp + "/members/" + f.adaID
		req("PUT", tm, "", nil, 401)
		req("PUT", tm, "", carl, 403)
		req("PUT", teamsPath+"/nosuch/members/"+f.adaID, "", ada, 404)
		req("PUT", tp+"/members/nosuchuser", "", ada, 403)
		req("PUT", tm, "", ada, 200)
		// Team members, and team roles held within a team.
		req("GET", tp+"/members", "", nil, 401)
		req("GET", tp+"/members", "", carl, 403)
		req("GET", tp+"/members?role=manager", "", ada, 200)
		req("GET", tp+"/members?role=wizard", "", ada, 400)
		req("GET", teamsPath+"/nosuch/members", "", ada, 404)
		role := fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.adaID)
		req("POST", tp+"/roles", role, withJSON(nil), 401)
		req("POST", tp+"/roles", role, withJSON(carl), 403)
		req("POST", tp+"/roles", `{"user": "x"}`, withJSON(ada), 400)
		req("POST", tp+"/roles", `{"user": "x", "role": "wizard"}`, withJSON(ada), 400)
		req("POST", teamsPath+"/nosuch/roles", role, withJSON(ada), 404)
		req("POST", tp+"/roles", role, withJSON(ada), 200)
		req("GET", tp+"/members?role=manager", "", ada, 200)
		// Fifty grants is the most a member holds.
		held, _ := f.svc.Store.OrgMember(context.Background(), id, f.adaID)
		saved := held.TeamRoles
		for i := len(held.TeamRoles); i < registry.MaxTeamRolesPerMember; i++ {
			held.TeamRoles = append(held.TeamRoles, teamGrant(fmt.Sprintf("filler%d", i), "delegate"))
		}
		if err := f.svc.Store.PutOrgMember(context.Background(), held); err != nil {
			t.Fatal(err)
		}
		req("POST", tp+"/roles", fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.adaID), withJSON(ada), 409)
		held.TeamRoles = saved
		if err := f.svc.Store.PutOrgMember(context.Background(), held); err != nil {
			t.Fatal(err)
		}
		req("PATCH", tp+"/roles", role, withJSON(nil), 401)
		req("PATCH", tp+"/roles", role, withJSON(carl), 403)
		req("PATCH", tp+"/roles", `{"user": "x", "role": "manager", "from": "soon"}`, withJSON(ada), 400)
		req("PATCH", tp+"/roles", fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.adaID), withJSON(ada), 404)
		req("PATCH", tp+"/roles", fmt.Sprintf(`{"user": %q, "role": "manager", "until": "2099-01-01T00:00:00Z"}`, f.adaID), withJSON(ada), 200)
		req("DELETE", tp+"/roles", role, withJSON(nil), 401)
		req("DELETE", tp+"/roles", role, withJSON(carl), 403)
		req("DELETE", tp+"/roles", `{}`, withJSON(ada), 400)
		req("DELETE", teamsPath+"/nosuch/roles", role, withJSON(ada), 404)
		req("DELETE", tp+"/roles", role, withJSON(ada), 200)
		f.svc.Settings.Organizations.MaxTeamsPerMember = 0
		req("PUT", tp+"/members/"+f.adaID, "", ada, 200) // already in: nothing to count
		f.svc.Settings.Organizations.MaxTeamsPerMember = registry.DefaultMaxTeamsPerMember
		req("DELETE", tm, "", nil, 401)
		req("DELETE", tm, "", carl, 403)
		req("DELETE", teamsPath+"/nosuch/members/"+f.adaID, "", ada, 404)
		req("DELETE", tm, "", ada, 200)
		bosses := req("POST", teamsPath, `{"name": "Bosses", "roles": ["owner"]}`, ada, 201)["id"].(string)
		f.svc.Settings.Organizations.MaxTeamsPerMember = 0
		req("PUT", teamsPath+"/"+bosses+"/members/"+f.adaID, "", ada, 409)
		f.svc.Settings.Organizations.MaxTeamsPerMember = registry.DefaultMaxTeamsPerMember
		req("DELETE", tp, "", nil, 401)
		req("DELETE", tp, "", carl, 403)
		req("DELETE", teamsPath+"/nosuch", "", ada, 404)
		req("DELETE", tp, "", ada, 204)
		req("GET", "/v1/nope/_orgs/x/teams", "", ada, 404)
		req("POST", "/v1/nope/_orgs/x/teams", `{"name": "x"}`, ada, 404)
		// A team that keeps the organization its manager can't lose that, by any route.
		mgr := req("POST", teamsPath, `{"name": "Managers", "roles": ["owner"]}`, ada, 201)["id"].(string)
		req("PUT", teamsPath+"/"+mgr+"/members/"+f.adaID, "", ada, 200)
		if _, err := f.svc.AdminSetMemberRoles(context.Background(), id, f.adaID, nil); err != nil {
			t.Fatal(err)
		}
		req("PATCH", teamsPath+"/"+mgr, `{"roles": []}`, withJSON(ada), 409)
		req("DELETE", teamsPath+"/"+mgr+"/members/"+f.adaID, "", ada, 409)
		req("DELETE", teamsPath+"/"+mgr, "", ada, 409)
		admMgr := adm + "/" + id + "/teams/" + mgr
		req("PATCH", admMgr, `{"roles": []}`, withJSON(key), 409)
		req("DELETE", admMgr+"/members/"+f.adaID, "", key, 409)
		req("DELETE", admMgr, "", key, 409)
		if _, err := f.svc.AdminSetMemberRoles(context.Background(), id, f.adaID, []string{"owner"}); err != nil {
			t.Fatal(err)
		}
		req("DELETE", teamsPath+"/"+mgr, "", ada, 204)
		// The only manager is kept: ada is the owner only directly, so the team of owners
		// going away or losing the role is fine, but the organization's last manager is not.
		req("PATCH", teamsPath+"/"+bosses, `{"roles": []}`, withJSON(ada), 200)
		req("DELETE", teamsPath+"/"+bosses, "", ada, 204)
		admTeams := adm + "/" + id + "/teams"
		req("GET", admTeams, "", nil, 401)
		req("GET", admTeams, "", ada, 403)
		req("GET", adm+"/nosuch/teams", "", key, 404)
		req("GET", admTeams, "", key, 200)
		req("POST", admTeams, `{"name": "Ops"}`, nil, 401)
		req("POST", admTeams, `{"name": "Ops"}`, ada, 403)
		req("POST", admTeams, `{}`, key, 400)
		req("POST", adm+"/nosuch/teams", `{"name": "Ops"}`, key, 404)
		opsTeam := req("POST", admTeams, `{"name": "Ops", "roles": ["owner"]}`, key, 201)["id"].(string)
		req("POST", admTeams, `{"name": "Ops"}`, key, 409)
		ap := admTeams + "/" + opsTeam
		req("PATCH", ap, `{"name": "Ops2"}`, withJSON(nil), 401)
		req("PATCH", ap, `{"name": "Ops2"}`, withJSON(ada), 403)
		req("PATCH", admTeams+"/nosuch", `{"name": "x"}`, withJSON(key), 404)
		req("PATCH", ap, `{"roles": ["wizard"]}`, withJSON(key), 400)
		req("PATCH", ap, `{"name": "Ops2"}`, withJSON(key), 200)
		am := ap + "/members/" + f.adaID
		req("PUT", am, "", nil, 401)
		req("PUT", am, "", ada, 403)
		req("PUT", admTeams+"/nosuch/members/"+f.adaID, "", key, 404)
		req("PUT", ap+"/members/nosuchuser", "", key, 404)
		req("PUT", am, "", key, 200)
		req("DELETE", am, "", key, 200)
		// Team members and team roles, for administrators.
		req("GET", ap+"/members", "", nil, 401)
		req("GET", ap+"/members", "", ada, 403)
		req("GET", ap+"/members?role=wizard", "", key, 400)
		req("GET", admTeams+"/nosuch/members", "", key, 404)
		req("GET", ap+"/members", "", key, 200)
		role = fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.adaID)
		req("POST", ap+"/roles", role, withJSON(nil), 401)
		req("POST", ap+"/roles", role, withJSON(ada), 403)
		req("POST", ap+"/roles", `{}`, withJSON(key), 400)
		req("POST", admTeams+"/nosuch/roles", role, withJSON(key), 404)
		req("POST", ap+"/roles", role, withJSON(key), 200)
		req("PATCH", ap+"/roles", `{"user": "`+f.adaID+`", "role": "delegate", "until": "2099-01-01T00:00:00Z"}`, withJSON(key), 200)
		held.TeamRoles = nil
		for i := 0; i < registry.MaxTeamRolesPerMember; i++ {
			held.TeamRoles = append(held.TeamRoles, teamGrant(fmt.Sprintf("filler%d", i), "delegate"))
		}
		if err := f.svc.Store.PutOrgMember(context.Background(), held); err != nil {
			t.Fatal(err)
		}
		req("POST", ap+"/roles", fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.adaID), withJSON(key), 409)
		held.TeamRoles = nil
		if err := f.svc.Store.PutOrgMember(context.Background(), held); err != nil {
			t.Fatal(err)
		}
		req("PATCH", ap+"/roles", role, withJSON(nil), 401)
		req("PATCH", ap+"/roles", role, withJSON(ada), 403)
		req("PATCH", ap+"/roles", `{"user": "x", "role": "delegate", "until": "soon"}`, withJSON(key), 400)
		req("PATCH", admTeams+"/nosuch/roles", role, withJSON(key), 404)
		req("DELETE", ap+"/roles", role, withJSON(nil), 401)
		req("DELETE", ap+"/roles", role, withJSON(ada), 403)
		req("DELETE", ap+"/roles", `{}`, withJSON(key), 400)
		req("DELETE", admTeams+"/nosuch/roles", role, withJSON(key), 404)
		req("DELETE", ap+"/roles", role, withJSON(key), 200)
		f.svc.Settings.Organizations.MaxTeamsPerMember = 0
		req("PUT", am, "", key, 409)
		f.svc.Settings.Organizations.MaxTeamsPerMember = registry.DefaultMaxTeamsPerMember
		req("DELETE", am, "", nil, 401)
		req("DELETE", am, "", ada, 403)
		req("DELETE", admTeams+"/nosuch/members/"+f.adaID, "", key, 404)
		req("DELETE", am, "", key, 200)
		req("DELETE", ap, "", nil, 401)
		req("DELETE", ap, "", ada, 403)
		req("DELETE", admTeams+"/nosuch", "", key, 404)
		req("DELETE", ap, "", key, 204)
		// Invitations. The realm's email limits are lifted for the sends and brought back for 429.
		inv := orgs + "/" + id + "/invitations"
		lim := &f.svc.Settings.Email.Limits
		oldLimits := *lim
		lim.PerIPPerHour, lim.PerKindPerHour, lim.PerDay = 100000, 100000, 100000
		defer func() { *lim = oldLimits }()
		req("POST", inv, `{"binding": "none"}`, nil, 401)
		req("POST", inv, `{"binding": "none"}`, carl, 403)
		req("POST", inv, `{}`, ada, 400)
		f.svc.Settings.Organizations.MaxPendingInvitations = 0
		req("POST", inv, `{"binding": "none"}`, ada, 409)
		f.svc.Settings.Organizations.MaxPendingInvitations = registry.DefaultMaxPendingInvitations
		withEmail := f.svc.Settings.Email
		f.svc.Settings.Email = nil
		req("POST", inv, `{"binding": "email", "email": "inv@example.com", "send": true}`, ada, 409)
		f.svc.Settings.Email = withEmail
		invited := req("POST", inv, `{"binding": "none", "roles": ["editor"], "max_uses": 3}`, ada, 201)
		invID, invTok := invited["id"].(string), invited["token"].(string)
		req("GET", inv, "", nil, 401)
		req("GET", inv, "", bob, 403)
		req("GET", inv, "", ada, 200)
		req("POST", orgs+"/invitations/preview", `{"token": "`+invTok+`"}`, nil, 401)
		req("POST", orgs+"/invitations/preview", `{"token": "nope"}`, ada, 400)
		boundTo := req("POST", inv, `{"binding": "user", "user": "`+f.bobID+`"}`, ada, 201)
		req("POST", orgs+"/invitations/preview", `{"token": "`+boundTo["token"].(string)+`"}`, carl, 403)
		req("POST", orgs+"/invitations/preview", `{"token": "`+invTok+`"}`, bob, 200)
		req("POST", orgs+"/invitations/accept", `{"token": "`+invTok+`"}`, nil, 401)
		req("POST", orgs+"/invitations/accept", `{"token": "nope"}`, bob, 400)
		req("POST", orgs+"/invitations/accept", `{"token": "`+boundTo["token"].(string)+`"}`, carl, 403)
		limitOne := f.svc.Settings.Organizations
		limitOne.MaxMembershipsPerUser = 1
		req("POST", orgs+"/invitations/accept", `{"token": "`+invTok+`"}`, bob, 409)
		limitOne.MaxMembershipsPerUser = registry.DefaultMaxMembershipsPerUser
		req("POST", orgs+"/invitations/accept", `{"token": "`+invTok+`"}`, bob, 200)
		req("POST", inv+"/"+invID+"/link", "", nil, 401)
		req("POST", inv+"/"+invID+"/link", "", bob, 403)
		req("POST", inv+"/nosuch/link", "", ada, 404)
		req("POST", inv+"/"+invID+"/link", "", ada, 200)
		req("POST", inv+"/"+invID+"/send", "", nil, 401)
		req("POST", inv+"/"+invID+"/send", "", bob, 403)
		req("POST", inv+"/nosuch/send", "", ada, 404)
		req("POST", inv+"/"+invID+"/send", "", ada, 400) // an open link has no address
		mailed := req("POST", inv, `{"binding": "email", "email": "mailed@example.com"}`, ada, 201)
		cipher := f.svc.Cipher
		f.svc.Cipher = nil // no secrets key: the instance can't seal the token
		req("POST", inv+"/"+mailed["id"].(string)+"/send", "", ada, 503)
		req("POST", inv, `{"binding": "email", "email": "sealed@example.com", "send": true}`, ada, 503)
		f.svc.Cipher = cipher
		req("POST", inv+"/"+mailed["id"].(string)+"/send", "", ada, 202)
		withMail := f.svc.Settings.Email
		f.svc.Settings.Email = nil
		req("POST", inv+"/"+mailed["id"].(string)+"/send", "", ada, 409)
		f.svc.Settings.Email = withMail
		lim.PerKindPerHour = 1 // the address got its one message of this kind
		req("POST", inv+"/"+mailed["id"].(string)+"/send", "", ada, 429)
		req("POST", inv, `{"binding": "email", "email": "mailed@example.com", "send": true}`, ada, 429)
		lim.PerKindPerHour = 100000
		req("DELETE", inv+"/"+invID, "", nil, 401)
		req("DELETE", inv+"/"+invID, "", bob, 403)
		req("DELETE", inv+"/nosuch", "", ada, 404)
		req("DELETE", inv+"/"+invID, "", ada, 204)
		req("GET", adm+"/"+id+"/invitations", "", nil, 401)
		req("GET", adm+"/"+id+"/invitations", "", ada, 403)
		req("GET", adm+"/nosuch/invitations", "", key, 404)
		req("GET", adm+"/"+id+"/invitations", "", key, 200)
		req("POST", adm+"/"+id+"/invitations", `{"binding": "none"}`, nil, 401)
		req("POST", adm+"/"+id+"/invitations", `{"binding": "none"}`, ada, 403)
		req("POST", adm+"/"+id+"/invitations", `{}`, key, 400)
		req("POST", adm+"/nosuch/invitations", `{"binding": "none"}`, key, 404)
		adminInv := req("POST", adm+"/"+id+"/invitations", `{"binding": "email", "email": "adm@example.com", "roles": ["owner"]}`, key, 201)
		aid := adm + "/" + id + "/invitations/" + adminInv["id"].(string)
		req("POST", aid+"/link", "", nil, 401)
		req("POST", aid+"/link", "", ada, 403)
		req("POST", adm+"/"+id+"/invitations/nosuch/link", "", key, 404)
		req("POST", aid+"/link", "", key, 200)
		req("DELETE", aid, "", nil, 401)
		req("DELETE", aid, "", ada, 403)
		req("DELETE", adm+"/"+id+"/invitations/nosuch", "", key, 404)
		req("POST", aid+"/link", "", key, 200)
		f.svc.Settings.Organizations.MaxPendingInvitations = 0
		req("POST", adm+"/"+id+"/invitations", `{"binding": "none"}`, key, 409)
		f.svc.Settings.Organizations.MaxPendingInvitations = registry.DefaultMaxPendingInvitations
		req("DELETE", aid, "", key, 204)
		req("POST", aid+"/link", "", key, 400) // revoked: nothing to renew
		req("POST", inv+"/"+invID+"/link", "", ada, 400)
		limit := f.svc.Settings.Organizations
		limit.MaxMembershipsPerUser = 1
		req("POST", adm+"/"+madeID+"/members", `{"user": "`+f.adaID+`"}`, key, 409)
		req("POST", adm+"/"+madeID+"/owner", `{"user": "`+f.adaID+`"}`, key, 409)
		limit.MaxMembershipsPerUser = registry.DefaultMaxMembershipsPerUser
		req("POST", adm+"/"+madeID+"/owner", `{"user": "`+f.adaID+`"}`, key, 200)
		req("DELETE", mem, "", key, 204)
		_ = other
	}

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
		{"data cacheable by shared caches", "GET", docPath, 200, jsonHdr("ETag", `"1"`, "Vary", "Origin, Authorization", "Cache-Control", "max-age=60"), doc, "Cache-Control"},
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

func ptrString(s string) *string { return &s }

// dirtyJPEGForContract is a JPEG cut short, which cannot be copied without its metadata.
func dirtyJPEGForContract(t *testing.T) []byte {
	t.Helper()
	dirty, _ := dirtyJPEG(t, 30, 10)
	return dirty[:len(dirty)-40]
}

// fakeTriggerAdmin is the dispatcher's recorded state, with one open gap.
type fakeTriggerAdmin struct {
	mu       sync.Mutex
	resolved bool
}

func (f *fakeTriggerAdmin) Status(context.Context) (triggers.DispatcherStatus, error) {
	return triggers.DispatcherStatus{Found: true, Held: true, Epoch: 3, Holder: "host-1", LeaseExpires: time.Now().Add(10 * time.Second), WatermarkAt: time.Now().Add(-2 * time.Second)}, nil
}

func (f *fakeTriggerAdmin) Gaps(_ context.Context, realm string, all bool, _ int) ([]triggers.GapRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := triggers.GapRecord{
		Gap: triggers.Gap{Realm: realm, Collection: "notes", Trigger: "on_paid", Reason: triggers.GapPreImage, From: time.Now().Add(-time.Hour), To: time.Now(), Count: 2, Keys: []string{"n1", "n2"}},
		ID:  "gap1", RecordedAt: time.Now(),
	}
	if f.resolved {
		if !all {
			return nil, nil
		}
		g.ResolvedAt = time.Now()
	}
	return []triggers.GapRecord{g}, nil
}

func (f *fakeTriggerAdmin) ResolveGap(_ context.Context, _, id string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "gap1" || f.resolved {
		return false, nil
	}
	f.resolved = true
	return true, nil
}
