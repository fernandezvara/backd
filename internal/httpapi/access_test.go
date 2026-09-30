package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// accessFixture serves realm "acme" (auth enabled, databases "app" and
// "crm") and realm "shop" (auth disabled), plus realm "other" (auth
// enabled) whose credentials must not work in acme.
type accessFixture struct {
	*fixture
	acme, other *auth.Users
	log         *bytes.Buffer
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"acme/realm.yaml":               "signup: open\n",
		"acme/app/items/schema.json":    itemSchema,
		"acme/crm/items/schema.json":    itemSchema,
		"other/realm.yaml":              "signup: open\n",
		"other/app/items/schema.json":   itemSchema,
		"shop/realm.yaml":               "auth: disabled\n",
		"shop/orders/items/schema.json": itemSchema,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	hasher := auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1})
	services := map[string]*auth.Users{}
	for _, name := range []string{"acme", "other"} {
		services[name] = &auth.Users{Store: authtest.NewMemStore(), Hasher: hasher, Settings: reg.Realms[name].Settings}
	}
	var buf bytes.Buffer
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(realm string) *auth.Users { return services[realm] }
		c.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	})
	return &accessFixture{fixture: f, acme: services["acme"], other: services["other"], log: &buf}
}

func TestDataRoutesRequireAPIKey(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()
	_, key, err := f.acme.CreateAPIKey(ctx, "billing", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := f.acme.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, _ := f.other.CreateAPIKey(ctx, "billing", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	_, otherSession, _ := f.other.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")

	// An API key works on every database of its realm.
	for _, base := range []string{"/v1/acme/app/items", "/v1/acme/crm/items"} {
		rec, out := f.doH(t, "POST", base, `{"name": "a"}`, bearer(key))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s with key: %d %v", base, rec.Code, out)
		}
		id := out["id"].(string)
		for _, m := range []struct{ method, path, body string }{
			{"GET", base, ""}, {"GET", base + "/" + id, ""}, {"PUT", base + "/" + id, `{"name": "b"}`},
			{"PATCH", base + "/" + id, `{"age": 1}`}, {"DELETE", base + "/" + id, ""},
		} {
			if rec, out := f.doH(t, m.method, m.path, m.body, bearer(key)); rec.Code >= 300 {
				t.Errorf("%s %s with key: %d %v", m.method, m.path, rec.Code, out)
			}
		}
	}

	tests := []struct {
		name     string
		hdr      map[string]string
		wantCode int
		wantErr  string
	}{
		// Users are recognized on every database of the realm, but no rule lets them in yet.
		{"session", bearer(session), http.StatusForbidden, "forbidden"},
		{"anonymous", nil, http.StatusUnauthorized, "unauthenticated"},
		{"other realm's key", bearer(otherKey), http.StatusUnauthorized, "unauthenticated"},
		{"other realm's session", bearer(otherSession), http.StatusUnauthorized, "unauthenticated"},
		{"unknown key", bearer("bdk_unknown"), http.StatusUnauthorized, "unauthenticated"},
		{"not bearer", map[string]string{"Authorization": "Basic " + key}, http.StatusUnauthorized, "unauthenticated"},
	}
	for _, tt := range tests {
		for _, base := range []string{"/v1/acme/app/items", "/v1/acme/crm/items"} {
			rec, out := f.doH(t, "GET", base, "", tt.hdr)
			if rec.Code != tt.wantCode || errCode(out) != tt.wantErr {
				t.Errorf("%s on %s: %d %v", tt.name, base, rec.Code, out)
			}
			if rec.Code == http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), `Bearer realm="acme"`) {
				t.Errorf("%s: WWW-Authenticate = %q", tt.name, rec.Header().Get("WWW-Authenticate"))
			}
		}
	}

	// Revoked keys stop working at once.
	if err := f.acme.RevokeAPIKey(ctx, "billing"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/items", "", bearer(key)); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", rec.Code)
	}
}

func TestDataRoutesAuthDisabledRealm(t *testing.T) {
	f := newAccessFixture(t)
	// No credentials needed; credentials are ignored.
	for _, hdr := range []map[string]string{nil, bearer("bdk_whatever")} {
		rec, out := f.doH(t, "POST", "/v1/shop/orders/items", `{"name": "a"}`, hdr)
		if rec.Code != http.StatusCreated {
			t.Errorf("auth-disabled realm: %d %v", rec.Code, out)
		}
	}
}

func TestDataRoutesActorLog(t *testing.T) {
	f := newAccessFixture(t)
	_, key, _ := f.acme.CreateAPIKey(context.Background(), "billing", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	for hdr, want := range map[string]string{key: `"actor":"key:billing"`, "": `"actor":"anonymous"`} {
		f.log.Reset()
		h := map[string]string{}
		if hdr != "" {
			h = bearer(hdr)
		}
		f.doH(t, "GET", "/v1/acme/app/items", "", h)
		if !strings.Contains(f.log.String(), want) {
			t.Errorf("log lacks %s: %s", want, f.log)
		}
		if hdr != "" && strings.Contains(f.log.String(), hdr[4:]) {
			t.Error("API key written to the log")
		}
	}
	f.log.Reset()
	f.doH(t, "GET", "/v1/shop/orders/items", "", nil)
	if strings.Contains(f.log.String(), `"actor"`) {
		t.Errorf("actor logged for an auth-disabled realm: %s", f.log)
	}
}

func TestDataRoutesExpiredKey(t *testing.T) {
	f := newAccessFixture(t)
	clock := time.Now()
	f.acme.Now = func() time.Time { return clock }
	_, key, _ := f.acme.CreateAPIKey(context.Background(), "temp", auth.KeyOptions{TTL: time.Hour, Role: auth.KeyRoleAdmin})
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/items", "", bearer(key)); rec.Code != http.StatusOK {
		t.Errorf("fresh key: %d", rec.Code)
	}
	clock = clock.Add(time.Hour)
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/items", "", bearer(key)); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired key: %d", rec.Code)
	}
}

func TestOwnershipFields(t *testing.T) {
	f := newAccessFixture(t)
	_, key, _ := f.acme.CreateAPIKey(context.Background(), "billing", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	base := "/v1/acme/app/items"

	rec, out := f.doH(t, "POST", base, `{"name": "a"}`, bearer(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	meta := out["_meta"].(map[string]any)
	if owner, has := meta["owner"]; !has || owner != nil || meta["created_by"] != "key:billing" || meta["updated_by"] != "key:billing" {
		t.Errorf("created _meta = %v", meta)
	}
	id := out["id"].(string)

	// A different key updates it: created_by and owner stay, updated_by changes.
	_, key2, _ := f.acme.CreateAPIKey(context.Background(), "worker", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	for _, m := range []struct{ method, body string }{{"PUT", `{"name": "b"}`}, {"PATCH", `{"age": 2}`}} {
		rec, out = f.doH(t, m.method, base+"/"+id, m.body, bearer(key2))
		meta = out["_meta"].(map[string]any)
		if rec.Code != http.StatusOK || meta["created_by"] != "key:billing" || meta["updated_by"] != "key:worker" || meta["owner"] != nil {
			t.Errorf("%s: %d _meta = %v", m.method, rec.Code, meta)
		}
	}

	// Ownership fields can be filtered and sorted on.
	rec, out = f.doH(t, "GET", base+`?where={"_meta.updated_by":"key:worker"}&order_by=_meta.created_by`, "", bearer(key))
	q := f.store.lastQuery
	if rec.Code != http.StatusOK || !reflect.DeepEqual(q.Filter, storage.And{storage.Condition{Field: "_meta.updated_by", Op: storage.OpEq, Value: "key:worker"}}) || q.Sort[0].Field != "_meta.created_by" {
		t.Errorf("query on ownership fields: %d %v %+v", rec.Code, out, q)
	}

	// Realms with auth disabled don't get ownership fields.
	_, out = f.do(t, "POST", "/v1/shop/orders/items", `{"name": "a"}`)
	for _, k := range []string{"owner", "created_by", "updated_by"} {
		if _, has := out["_meta"].(map[string]any)[k]; has {
			t.Errorf("auth-disabled realm got _meta.%s", k)
		}
	}
}

func TestOwnershipLegacyDocument(t *testing.T) {
	f := newAccessFixture(t)
	_, key, _ := f.acme.CreateAPIKey(context.Background(), "billing", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	// A document stored before ownership existed.
	f.store.put("acme__app", "items", map[string]any{
		"id": "legacy1", "name": "old",
		"_meta": map[string]any{"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(), "version": int64(1)},
	})
	rec, out := f.doH(t, "PATCH", "/v1/acme/app/items/legacy1", `{"age": 1}`, bearer(key))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %v", rec.Code, out)
	}
	meta := out["_meta"].(map[string]any)
	_, hasOwner := meta["owner"]
	_, hasCreatedBy := meta["created_by"]
	if hasOwner || hasCreatedBy || meta["updated_by"] != "key:billing" {
		t.Errorf("legacy document _meta after write = %v", meta)
	}
}

// put stores a document directly, bypassing the API.
func (s *memStore) put(mongoDB, coll string, doc map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &memRepo{s: s, key: mongoDB + "." + coll}
	r.coll()[doc["id"].(string)] = doc
}
