package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const itemSchema = `{
  "type": "object",
  "properties": {
    "name":    {"type": "string", "minLength": 1},
    "age":     {"type": "integer", "minimum": 0},
    "price":   {"type": "number"},
    "email":   {"type": "string", "format": "email"},
    "note":    {"type": ["string", "null"]},
    "address": {"type": "object", "properties": {"city": {"type": "string"}, "zip": {"type": "string"}}, "additionalProperties": false}
  },
  "required": ["name"],
  "additionalProperties": false
}`

// memStore is an in-memory storage backend for handler tests.
type memStore struct {
	mu        sync.Mutex
	lastQuery storage.Query
	// beforeWrite, when set, runs inside Replace before the version check,
	// to simulate concurrent writers.
	beforeWrite func(docs map[string]storage.Document)
	docs        map[string]map[string]storage.Document // collection key → id → doc
	fail        error
}

func (s *memStore) Repository(c *registry.Collection) storage.Repository {
	return &memRepo{s: s, key: c.MongoDatabase + "." + c.Name}
}

// Transact fakes atomicity by snapshotting every collection before fn
// runs and restoring it if fn fails, so batch's "all or nothing" is
// observable in tests without a real MongoDB transaction.
func (s *memStore) Transact(ctx context.Context, fn func(context.Context) error) error {
	s.mu.Lock()
	snapshot := make(map[string]map[string]storage.Document, len(s.docs))
	for coll, docs := range s.docs {
		cp := make(map[string]storage.Document, len(docs))
		for id, doc := range docs {
			cp[id] = clone(doc).(map[string]any)
		}
		snapshot[coll] = cp
	}
	s.mu.Unlock()
	if err := fn(ctx); err != nil {
		s.mu.Lock()
		s.docs = snapshot
		s.mu.Unlock()
		return err
	}
	return nil
}

type memRepo struct {
	s   *memStore
	key string
}

func (r *memRepo) coll() map[string]storage.Document {
	if r.s.docs == nil {
		r.s.docs = map[string]map[string]storage.Document{}
	}
	if r.s.docs[r.key] == nil {
		r.s.docs[r.key] = map[string]storage.Document{}
	}
	return r.s.docs[r.key]
}

// clone round-trips through JSON-like copying to avoid aliasing.
func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = clone(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = clone(e)
		}
		return s
	}
	return v
}

func (r *memRepo) Create(_ context.Context, doc storage.Document) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return r.s.fail
	}
	r.coll()[doc["id"].(string)] = clone(doc).(map[string]any)
	return nil
}

func (r *memRepo) Get(_ context.Context, id string) (storage.Document, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return nil, r.s.fail
	}
	d, ok := r.coll()[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return clone(d).(map[string]any), nil
}

func (r *memRepo) List(_ context.Context, q storage.Query) (storage.Page, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return storage.Page{}, r.s.fail
	}
	r.s.lastQuery = q
	var ids []string
	for id, doc := range r.coll() {
		if (q.Filter == nil || matches(doc, q.Filter)) && (q.Access == nil || matches(doc, q.Access)) {
			ids = append(ids, id)
		}
	}
	sortStrings(ids)
	var page storage.Page
	if q.Count {
		n := int64(len(ids))
		page.Total = &n
	}
	for i := q.Skip; i < len(ids); i++ {
		if len(page.Items) == q.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, clone(r.coll()[ids[i]]).(map[string]any))
	}
	return page, nil
}

func (r *memRepo) Replace(_ context.Context, doc storage.Document, ifVersion int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.beforeWrite != nil {
		r.s.beforeWrite(r.coll())
	}
	id := doc["id"].(string)
	cur, ok := r.coll()[id]
	if !ok {
		return storage.ErrNotFound
	}
	if version(cur) != ifVersion {
		return storage.ErrVersionMismatch
	}
	r.coll()[id] = clone(doc).(map[string]any)
	return nil
}

func (r *memRepo) Delete(_ context.Context, id string, ifVersion *int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return r.s.fail
	}
	if r.s.beforeWrite != nil {
		r.s.beforeWrite(r.coll())
	}
	cur, ok := r.coll()[id]
	if !ok {
		return storage.ErrNotFound
	}
	if ifVersion != nil && version(cur) != *ifVersion {
		return storage.ErrVersionMismatch
	}
	delete(r.coll(), id)
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

type fixture struct {
	h     http.Handler
	store *memStore
	clock *time.Time
	// users are the auth services of auth-enabled realms (MongoDB fixtures).
	users map[string]*auth.Users
}

func newFixture(t *testing.T, opts ...func(*Config)) *fixture {
	t.Helper()
	return newFixtureWith(t, itemsRegistry(t), &memStore{}, opts...)
}

// itemsRegistry loads a config with one realm (auth disabled) and the
// shop/orders/items collection.
func itemsRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "shop", "orders", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(itemSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shop", registry.RealmFile), []byte("auth: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func newFixtureWith(t *testing.T, reg *registry.Registry, store Store, opts ...func(*Config)) *fixture {
	t.Helper()
	clock := time.Date(2026, 9, 25, 21, 30, 0, 123456789, time.UTC)
	f := &fixture{clock: &clock}
	if ms, ok := store.(*memStore); ok {
		f.store = ms
	}
	cfg := Config{
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry: reg,
		Store:    store,
		Ready:    func(context.Context) error { return nil },
		Now:      func() time.Time { return *f.clock },
	}
	for _, o := range opts {
		o(&cfg)
	}
	h := NewHandler(cfg)
	f.h = h
	return f
}

func (f *fixture) do(t *testing.T, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	setContentType(req)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: response is not a JSON object: %q", method, path, rec.Body)
		}
	}
	return rec, out
}

func errorOf(body map[string]any) (code string, details []any) {
	e, _ := body["error"].(map[string]any)
	code, _ = e["code"].(string)
	details, _ = e["details"].([]any)
	return code, details
}

const items = "/v1/shop/orders/items"

func TestCreateAndGet(t *testing.T) {
	f := newFixture(t)
	rec, body := f.do(t, "POST", items, `{"name": "Widget", "age": 3, "price": 12.5, "id": "mine", "_meta": {"x": 1}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id, _ := body["id"].(string)
	if len(id) != 20 || id == "mine" {
		t.Errorf("id = %q, want a server-generated xid", id)
	}
	if loc := rec.Header().Get("Location"); loc != items+"/"+id {
		t.Errorf("Location = %q", loc)
	}
	wantMeta := map[string]any{"created_at": "2026-09-25T21:30:00.123Z", "updated_at": "2026-09-25T21:30:00.123Z", "version": float64(1)}
	if got := rec.Header().Get("ETag"); got != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", got)
	}
	if !reflect.DeepEqual(body["_meta"], wantMeta) {
		t.Errorf("_meta = %v, want %v", body["_meta"], wantMeta)
	}
	if body["name"] != "Widget" || body["age"] != float64(3) || body["price"] != 12.5 {
		t.Errorf("unexpected body %v", body)
	}

	// Numbers are stored as int64 / float64.
	stored := f.store.docs["shop__orders.items"][id]
	if _, ok := stored["age"].(int64); !ok {
		t.Errorf("age stored as %T, want int64", stored["age"])
	}
	if _, ok := stored["price"].(float64); !ok {
		t.Errorf("price stored as %T, want float64", stored["price"])
	}

	rec, got := f.do(t, "GET", items+"/"+id, "")
	if rec.Code != http.StatusOK || !reflect.DeepEqual(got, body) || rec.Header().Get("ETag") != `"1"` {
		t.Errorf("get = %d %v (ETag %s), want %v", rec.Code, got, rec.Header().Get("ETag"), body)
	}
}

func TestCreateValidationErrors(t *testing.T) {
	f := newFixture(t)
	rec, body := f.do(t, "POST", items, `{"age": -1, "email": "nope", "extra": 1, "address": {"city": 5}}`)
	code, details := errorOf(body)
	if rec.Code != http.StatusBadRequest || code != codeValidation {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	got := map[string]string{}
	for _, d := range details {
		m := d.(map[string]any)
		got[m["path"].(string)] = m["reason"].(string)
	}
	for path, reason := range map[string]string{
		"name":         "is required",
		"extra":        "is not allowed",
		"age":          "minimum",
		"email":        "email",
		"address.city": "string",
	} {
		if !strings.Contains(got[path], reason) {
			t.Errorf("detail for %q = %q, want it to mention %q (all: %v)", path, got[path], reason, got)
		}
	}
}

func TestInvalidJSON(t *testing.T) {
	f := newFixture(t)
	for _, body := range []string{"", "{", "[]", `"x"`, `{} {}`} {
		rec, out := f.do(t, "POST", items, body)
		if code, _ := errorOf(out); rec.Code != http.StatusBadRequest || code != codeInvalidJSON {
			t.Errorf("body %q = %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestUnknownResources(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/v1/nope/orders/items", "/v1/shop/nope/items", "/v1/shop/orders/nope",
		"/v1/shop/_auth/login", items + "/missing",
	} {
		rec, out := f.do(t, "GET", path, "")
		if code, _ := errorOf(out); rec.Code != http.StatusNotFound || code != codeNotFound {
			t.Errorf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
	}
	rec, _ := f.do(t, "POST", items+"/x", `{}`)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST on document = %d", rec.Code)
	}
}

func TestList(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for range 25 {
		_, b := f.do(t, "POST", items, `{"name": "x"}`)
		ids = append(ids, b["id"].(string))
	}

	rec, body := f.do(t, "GET", items, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if n := len(body["items"].([]any)); n != 20 || body["limit"] != float64(20) || body["skip"] != float64(0) || body["has_more"] != true {
		t.Errorf("default page: %d items, %v", n, body)
	}
	if _, ok := body["total"]; ok {
		t.Error("total present without count=true")
	}

	_, body = f.do(t, "GET", items+"?limit=10&skip=20", "")
	list := body["items"].([]any)
	if len(list) != 5 || body["has_more"] != false || list[0].(map[string]any)["id"] != ids[20] {
		t.Errorf("last page: %v", body)
	}

	_, body = f.do(t, "GET", items+"/?limit=1", "")
	if len(body["items"].([]any)) != 1 {
		t.Errorf("trailing slash list: %v", body)
	}

	for _, q := range []string{"limit=0", "limit=101", "limit=x", "skip=-1", "sort=name", "limit=1&limit=2"} {
		rec, out := f.do(t, "GET", items+"?"+q, "")
		if code, _ := errorOf(out); rec.Code != http.StatusBadRequest || code != codeInvalidQuery {
			t.Errorf("?%s = %d %s", q, rec.Code, rec.Body)
		}
	}
}

func TestListEmpty(t *testing.T) {
	f := newFixture(t)
	_, body := f.do(t, "GET", items, "")
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("empty list items = %#v, want []", body["items"])
	}
}

func TestReplace(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a", "age": 1}`)
	id := created["id"].(string)
	*f.clock = f.clock.Add(time.Minute)

	rec, body := f.do(t, "PUT", items+"/"+id, `{"name": "b", "id": "other", "_meta": {"created_at": "1999-01-01T00:00:00Z"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	meta := body["_meta"].(map[string]any)
	if body["id"] != id || body["name"] != "b" || body["age"] != nil {
		t.Errorf("put body = %v", body)
	}
	if meta["created_at"] != "2026-09-25T21:30:00.123Z" || meta["updated_at"] != "2026-09-25T21:31:00.123Z" || meta["version"] != float64(2) {
		t.Errorf("put _meta = %v", meta)
	}
	if got := rec.Header().Get("ETag"); got != `"2"` {
		t.Errorf("put ETag = %q", got)
	}

	rec, out := f.do(t, "PUT", items+"/"+id, `{"age": 1}`)
	if code, _ := errorOf(out); rec.Code != http.StatusBadRequest || code != codeValidation {
		t.Errorf("invalid put = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, "PUT", items+"/missing", `{"name": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("put missing = %d", rec.Code)
	}
}

func TestPatch(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a", "age": 1, "note": "n", "address": {"city": "X", "zip": "1"}}`)
	id := created["id"].(string)
	*f.clock = f.clock.Add(time.Second)

	rec, body := f.do(t, "PATCH", items+"/"+id, `{"age": 2, "note": null, "address": {"zip": "2"}, "id": "x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	want := map[string]any{
		"id": id, "name": "a", "age": float64(2), "address": map[string]any{"city": "X", "zip": "2"},
		"_meta": map[string]any{"created_at": "2026-09-25T21:30:00.123Z", "updated_at": "2026-09-25T21:30:01.123Z", "version": float64(2)},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("patch body = %v\nwant %v", body, want)
	}

	// The merged document is validated.
	rec, out := f.do(t, "PATCH", items+"/"+id, `{"name": null}`)
	if code, details := errorOf(out); rec.Code != http.StatusBadRequest || code != codeValidation || len(details) != 1 {
		t.Errorf("invalid patch = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, "PATCH", items+"/missing", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("patch missing = %d", rec.Code)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	id := created["id"].(string)
	if rec, _ := f.do(t, "DELETE", items+"/"+id, ""); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("delete = %d %q", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, "DELETE", items+"/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}

func TestStorageErrors(t *testing.T) {
	f := newFixture(t)
	f.store.fail = storage.ErrUnavailable
	rec, out := f.do(t, "GET", items, "")
	if code, _ := errorOf(out); rec.Code != http.StatusServiceUnavailable || code != codeUnavailable {
		t.Errorf("unavailable = %d %s", rec.Code, rec.Body)
	}
	f.store.fail = &storage.ConflictError{Fields: []string{"email", "name"}}
	rec, out = f.do(t, "POST", items, `{"name": "a"}`)
	code, details := errorOf(out)
	if rec.Code != http.StatusConflict || code != codeConflict || len(details) != 2 ||
		details[0].(map[string]any)["path"] != "email" ||
		!strings.Contains(details[0].(map[string]any)["reason"].(string), "email, name") {
		t.Errorf("conflict = %d %s", rec.Code, rec.Body)
	}
	f.store.fail = errors.New("boom")
	rec, out = f.do(t, "POST", items, `{"name": "a"}`)
	if code, _ := errorOf(out); rec.Code != http.StatusInternalServerError || code != codeInternal || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("internal = %d %s", rec.Code, rec.Body)
	}
}

func TestListQueryParameters(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", items, `{"name": "a", "age": 2}`)

	rec, body := f.do(t, "GET", items+`?where={"age":{"$gte":1},"name":"a"}&order_by=-age&count=true`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if body["total"] != float64(1) {
		t.Errorf("total = %v, want 1", body["total"])
	}
	want := storage.Query{
		Filter: storage.And{
			storage.Condition{Field: "age", Op: storage.OpGte, Value: int64(1)},
			storage.Condition{Field: "name", Op: storage.OpEq, Value: "a"},
		},
		Sort:  []storage.SortField{{Field: "age", Desc: true}, {Field: "id"}},
		Limit: 20,
		Count: true,
	}
	if !reflect.DeepEqual(f.store.lastQuery, want) {
		t.Errorf("query = %+v\nwant %+v", f.store.lastQuery, want)
	}

	// Injection attempts and bad queries are rejected before reaching storage.
	for _, q := range []string{
		`where={"$where":"sleep(100)"}`,
		`where={"name":{"$regex":".*"}}`,
		`where={"name":{"$expr":{}}}`,
		`where={"address":{"city":{"$gt":""}}}`,
		`where={"secret":1}`,
		`where={"age":"1"}`,
		`where=not-json`,
		`order_by=secret`,
		`count=yes`,
	} {
		rec, out := f.do(t, "GET", items+"?"+q, "")
		code, details := errorOf(out)
		if rec.Code != http.StatusBadRequest || code != codeInvalidQuery || len(details) == 0 {
			t.Errorf("?%s = %d %s", q, rec.Code, rec.Body)
		}
	}
}

func TestParseIfMatch(t *testing.T) {
	tests := []struct {
		in       []string
		specific bool
		match    map[int64]bool
	}{
		{nil, false, map[int64]bool{1: true, 7: true}},
		{[]string{`*`}, false, map[int64]bool{1: true}},
		{[]string{`"3"`}, true, map[int64]bool{3: true, 2: false}},
		{[]string{`"1", "2"`, `"5"`}, true, map[int64]bool{1: true, 2: true, 5: true, 3: false}},
		{[]string{`W/"3"`}, true, map[int64]bool{3: false}},
	}
	for _, tt := range tests {
		c, err := parseIfMatch(tt.in)
		if err != nil {
			t.Fatalf("parseIfMatch(%q): %v", tt.in, err)
		}
		if c.specific() != tt.specific {
			t.Errorf("parseIfMatch(%q).specific() = %v", tt.in, c.specific())
		}
		for v, want := range tt.match {
			if c.matches(v) != want {
				t.Errorf("parseIfMatch(%q).matches(%d) = %v", tt.in, v, !want)
			}
		}
	}
	for _, bad := range []string{`3`, `"x"`, `"-1"`, `"`, `""`} {
		if _, err := parseIfMatch([]string{bad}); err == nil {
			t.Errorf("parseIfMatch(%q) accepted", bad)
		}
	}
}

func (f *fixture) doH(t *testing.T, method, path, body string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	setContentType(req)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestIfMatch(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	doc := items + "/" + created["id"].(string)

	for _, tc := range []struct {
		method, body, ifMatch string
		wantCode              int
		wantETag              string
	}{
		{"PUT", `{"name": "b"}`, `"2"`, http.StatusPreconditionFailed, `"1"`},
		{"PUT", `{"name": "b"}`, `"1"`, http.StatusOK, `"2"`},
		{"PATCH", `{"age": 1}`, `"1"`, http.StatusPreconditionFailed, `"2"`},
		{"PATCH", `{"age": 1}`, `"1", "2"`, http.StatusOK, `"3"`},
		{"PATCH", `{"age": 2}`, `*`, http.StatusOK, `"4"`},
		{"PATCH", `{"age": 3}`, `W/"4"`, http.StatusPreconditionFailed, `"4"`},
		{"PATCH", `{"age": 3}`, `4`, http.StatusBadRequest, ""},
		{"DELETE", ``, `"3"`, http.StatusPreconditionFailed, `"4"`},
	} {
		rec, out := f.doH(t, tc.method, doc, tc.body, map[string]string{"If-Match": tc.ifMatch})
		if rec.Code != tc.wantCode || rec.Header().Get("ETag") != tc.wantETag {
			t.Errorf("%s If-Match %s = %d ETag %s, want %d ETag %s (%s)", tc.method, tc.ifMatch, rec.Code, rec.Header().Get("ETag"), tc.wantCode, tc.wantETag, rec.Body)
		}
		if tc.wantCode == http.StatusPreconditionFailed {
			if code, _ := errorOf(out); code != codeVersionMismatch {
				t.Errorf("412 code = %q", code)
			}
		}
	}
	if rec, _ := f.doH(t, "DELETE", doc, "", map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusNoContent {
		t.Errorf("delete with matching If-Match = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "PATCH", doc, `{}`, map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusNotFound {
		t.Errorf("patch deleted document = %d", rec.Code)
	}
}

func TestWriteRetriesAndConflicts(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	id := created["id"].(string)
	doc := items + "/" + id

	// A concurrent writer bumps the version before the first write lands:
	// PATCH without If-Match retries and keeps both changes.
	bumps := 1
	f.store.beforeWrite = func(docs map[string]storage.Document) {
		if bumps > 0 {
			bumps--
			d := docs[id]
			d["age"] = int64(9)
			d["_meta"].(map[string]any)["version"] = version(d) + 1
		}
	}
	rec, body := f.do(t, "PATCH", doc, `{"note": "x"}`)
	if rec.Code != http.StatusOK || body["age"] != float64(9) || body["note"] != "x" || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("retried patch = %d %v ETag %s", rec.Code, body, rec.Header().Get("ETag"))
	}

	// With a specific If-Match the same race is a 412, not a retry.
	bumps = 1
	rec, out := f.doH(t, "PATCH", doc, `{"note": "y"}`, map[string]string{"If-Match": `"3"`})
	if code, _ := errorOf(out); rec.Code != http.StatusPreconditionFailed || code != codeVersionMismatch {
		t.Errorf("raced patch with If-Match = %d %s", rec.Code, rec.Body)
	}

	// Losing every attempt gives up with 409 write_conflict.
	bumps = maxWriteAttempts
	rec, out = f.do(t, "PUT", doc, `{"name": "z"}`)
	if code, _ := errorOf(out); rec.Code != http.StatusConflict || code != codeWriteConflict {
		t.Errorf("exhausted retries = %d %s", rec.Code, rec.Body)
	}
}

func TestLegacyDocumentWithoutVersion(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	id := created["id"].(string)
	delete(f.store.docs["shop__orders.items"][id]["_meta"].(map[string]any), "version")

	rec, _ := f.do(t, "GET", items+"/"+id, "")
	if rec.Header().Get("ETag") != `"0"` {
		t.Errorf("legacy ETag = %q", rec.Header().Get("ETag"))
	}
	rec, body := f.doH(t, "PATCH", items+"/"+id, `{"age": 1}`, map[string]string{"If-Match": `"0"`})
	if rec.Code != http.StatusOK || body["_meta"].(map[string]any)["version"] != float64(1) {
		t.Errorf("legacy patch = %d %v", rec.Code, body)
	}
}

// setContentType sets the Content-Type backd expects for the method.
func setContentType(req *http.Request) {
	switch req.Method {
	case "POST", "PUT":
		req.Header.Set("Content-Type", "application/json")
	case "PATCH":
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
}

func TestContentType(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	doc := items + "/" + created["id"].(string)

	for _, tc := range []struct {
		method, path, ct string
		want             int
	}{
		{"POST", items, "", http.StatusUnsupportedMediaType},
		{"POST", items, "text/plain", http.StatusUnsupportedMediaType},
		{"POST", items, "application/merge-patch+json", http.StatusUnsupportedMediaType},
		{"POST", items, "application/json; charset=latin1", http.StatusUnsupportedMediaType},
		{"POST", items, "application/json; charset=UTF-8", http.StatusCreated},
		{"POST", items, "Application/JSON", http.StatusCreated},
		{"PUT", doc, "application/merge-patch+json", http.StatusUnsupportedMediaType},
		{"PUT", doc, "application/json", http.StatusOK},
		{"PATCH", doc, "application/json", http.StatusUnsupportedMediaType},
		{"PATCH", doc, "application/merge-patch+json;charset=utf-8", http.StatusOK},
		// Unknown collections and methods are reported before the media type.
		{"POST", "/v1/shop/orders/nope", "", http.StatusNotFound},
		{"POST", doc, "", http.StatusMethodNotAllowed},
	} {
		rec, out := f.doH(t, tc.method, tc.path, `{"name": "b"}`, map[string]string{"Content-Type": tc.ct})
		if rec.Code != tc.want {
			t.Errorf("%s %s with %q = %d, want %d (%s)", tc.method, tc.path, tc.ct, rec.Code, tc.want, rec.Body)
		}
		if tc.want == http.StatusUnsupportedMediaType {
			if code, _ := errorOf(out); code != codeUnsupportedMedia {
				t.Errorf("415 code = %q", code)
			}
		}
	}
}

func TestBodyLimit(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxBodyBytes = 64 })
	small := `{"name": "a"}`
	big := `{"name": "` + strings.Repeat("x", 64) + `"}`

	if rec, _ := f.do(t, "POST", items, small); rec.Code != http.StatusCreated {
		t.Errorf("small body = %d", rec.Code)
	}
	rec, out := f.do(t, "POST", items, big)
	if code, _ := errorOf(out); rec.Code != http.StatusRequestEntityTooLarge || code != codePayloadTooLarge {
		t.Errorf("declared oversize body = %d %s", rec.Code, rec.Body)
	}

	// A body without a declared length is cut off while reading.
	req := httptest.NewRequest("POST", items, io.MultiReader(strings.NewReader(big)))
	req.ContentLength = -1
	setContentType(req)
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("streamed oversize body = %d %s", rec.Code, rec.Body)
	}
}

// slowStore blocks every operation until the request context ends.
type slowStore struct{}

func (slowStore) Repository(*registry.Collection) storage.Repository { return slowRepo{} }
func (slowStore) Transact(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type slowRepo struct{}

func (slowRepo) wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (r slowRepo) Create(ctx context.Context, _ storage.Document) error {
	return r.wait(ctx)
}
func (r slowRepo) Get(ctx context.Context, _ string) (storage.Document, error) {
	return nil, r.wait(ctx)
}
func (r slowRepo) List(ctx context.Context, _ storage.Query) (storage.Page, error) {
	return storage.Page{}, r.wait(ctx)
}
func (r slowRepo) Replace(ctx context.Context, _ storage.Document, _ int64) error {
	return r.wait(ctx)
}
func (r slowRepo) Delete(ctx context.Context, _ string, _ *int64) error { return r.wait(ctx) }

func TestOpTimeout(t *testing.T) {
	f := newFixtureWith(t, itemsRegistry(t), slowStore{}, func(c *Config) { c.OpTimeout = 50 * time.Millisecond })

	start := time.Now()
	rec, out := f.do(t, "GET", items+"/x", "")
	if code, _ := errorOf(out); rec.Code != http.StatusServiceUnavailable || code != codeUnavailable {
		t.Errorf("slow storage = %d %s", rec.Code, rec.Body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("request took %s despite a 50ms op timeout", elapsed)
	}
}
