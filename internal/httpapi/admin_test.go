package httpapi

import (
	"context"
	"github.com/fernandezvara/backd/internal/auth"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const admin = "/v1/acme/_admin"

func TestAdminAccess(t *testing.T) {
	f := newRulesFixture(t)
	_, dataKey, err := f.svc.CreateAPIKey(context.Background(), "service", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		hdr      map[string]string
		want     int
		wantCode string
	}{
		{"anonymous", nil, http.StatusUnauthorized, "unauthenticated"},
		{"session", bearer(f.ada), http.StatusForbidden, "forbidden"},
		{"bad key", bearer("bdk_nope"), http.StatusUnauthorized, "unauthenticated"},
		{"on behalf", map[string]string{"Authorization": "Bearer " + f.key, "X-Backd-On-Behalf-Of": f.adaID}, http.StatusBadRequest, "invalid_header"},
		{"data key", bearer(dataKey), http.StatusForbidden, "forbidden"},
		{"admin key", bearer(f.key), http.StatusOK, ""},
	} {
		rec, out := f.doH(t, "GET", admin+"/users", "", tt.hdr)
		if rec.Code != tt.want || errCode(out) != tt.wantCode {
			t.Errorf("%s: %d %v", tt.name, rec.Code, out)
		}
	}
	if rec, _ := f.doH(t, "GET", "/v1/nope/_admin/users", "", bearer(f.key)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown realm: %d", rec.Code)
	}
	// Sessions of users holding an admin role (admin: true in realm.yaml)
	// are admitted; other roles, like "admin" here, don't count.
	if err := f.svc.AddRole(context.Background(), "bob@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", admin+"/users", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("session with a non-admin role: %d %v", rec.Code, out)
	}
	if err := f.svc.AddRole(context.Background(), "bob@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", admin+"/users", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("admin session: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", admin+"/users", "", map[string]string{"Authorization": "Bearer " + f.bob, "X-Backd-On-Behalf-Of": f.adaID}); rec.Code != http.StatusBadRequest {
		t.Errorf("admin session acting on behalf: %d %v", rec.Code, out)
	}
	if err := f.svc.RemoveRole(context.Background(), "bob@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "GET", admin+"/users", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("after losing the admin role: %d", rec.Code)
	}

	// Data keys keep full access to data.
	if rec, out := f.doH(t, "GET", posts, "", bearer(dataKey)); rec.Code != http.StatusOK {
		t.Errorf("data key on data: %d %v", rec.Code, out)
	}
}

func TestAdminUsers(t *testing.T) {
	f := newRulesFixture(t)
	key := func(extra ...string) map[string]string {
		h := map[string]string{"Authorization": "Bearer " + f.key}
		if len(extra) > 0 {
			h["Content-Type"] = "application/json"
		}
		return h
	}

	// List and exact email lookup.
	rec, out := f.doH(t, "GET", admin+"/users?limit=2", "", key())
	if items, _ := out["items"].([]any); rec.Code != http.StatusOK || len(items) != 2 || out["has_more"] != true {
		t.Errorf("list: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", admin+"/users?email=BOB@example.com", "", key())
	if items, _ := out["items"].([]any); rec.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != f.bobID {
		t.Errorf("lookup: %d %v", rec.Code, out)
	}
	if _, out = f.doH(t, "GET", admin+"/users?email=nobody@example.com", "", key()); len(out["items"].([]any)) != 0 {
		t.Errorf("lookup of unknown email: %v", out)
	}
	if rec, _ = f.doH(t, "GET", admin+"/users?page=2", "", key()); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown parameter: %d", rec.Code)
	}

	// Create, with and without a password.
	rec, out = f.doH(t, "POST", admin+"/users", `{"email": "Dan@Example.com", "password": "dev-p4ssw0rd!"}`, key(""))
	if rec.Code != http.StatusCreated || out["email"] != "dan@example.com" || out["disabled"] != false {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	dan := out["id"].(string)
	if _, _, err := f.svc.Login(context.Background(), "dan@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Errorf("created user can't log in: %v", err)
	}
	if rec, _ = f.doH(t, "POST", admin+"/users", `{"email": "eve@example.com"}`, key("")); rec.Code != http.StatusCreated {
		t.Errorf("create without password: %d", rec.Code)
	}
	for body, want := range map[string]int{
		`{"email": "dan@example.com"}`:                http.StatusConflict,
		`{"email": "nope"}`:                           http.StatusBadRequest,
		`{"email": "x@example.com", "password": "a"}`: http.StatusBadRequest,
		`{"email": "x@example.com", "roles": []}`:     http.StatusBadRequest,
	} {
		if rec, out := f.doH(t, "POST", admin+"/users", body, key("")); rec.Code != want {
			t.Errorf("create %s: %d %v", body, rec.Code, out)
		}
	}

	// Get, update flags, never the email.
	if rec, out = f.doH(t, "GET", admin+"/users/"+dan, "", key()); rec.Code != http.StatusOK || out["email"] != "dan@example.com" {
		t.Errorf("get: %d %v", rec.Code, out)
	}
	if rec, _ = f.doH(t, "GET", admin+"/users/nope", "", key()); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: %d", rec.Code)
	}
	rec, out = f.doH(t, "PATCH", admin+"/users/"+dan, `{"email_verified": true, "disabled": true}`, key(""))
	if rec.Code != http.StatusOK || out["email_verified"] != true || out["disabled"] != true {
		t.Errorf("patch: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "PATCH", admin+"/users/"+dan, `{"email": "other@example.com"}`, key(""))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "can't be changed") {
		t.Errorf("patch email: %d %v", rec.Code, out)
	}
	if rec, _ = f.doH(t, "PATCH", admin+"/users/"+dan, `{"disabled": "yes"}`, key("")); rec.Code != http.StatusBadRequest {
		t.Errorf("patch wrong type: %d", rec.Code)
	}
	f.doH(t, "PATCH", admin+"/users/"+dan, `{"disabled": false}`, key(""))

	// Password: sessions end.
	_, tok, _ := f.svc.Login(context.Background(), "dan@example.com", "dev-p4ssw0rd!", "")
	if rec, _ = f.doH(t, "POST", admin+"/users/"+dan+"/password", `{"password": "dev-p4ssw0rd!2"}`, key("")); rec.Code != http.StatusNoContent {
		t.Errorf("set password: %d", rec.Code)
	}
	if _, err := f.svc.Authenticate(context.Background(), tok); err == nil {
		t.Error("session survived a password set by an admin")
	}
	if rec, _ = f.doH(t, "POST", admin+"/users/"+dan+"/password", `{"password": "short"}`, key("")); rec.Code != http.StatusBadRequest {
		t.Errorf("weak password: %d", rec.Code)
	}

	// Roles.
	rec, out = f.doH(t, "PUT", admin+"/users/"+dan+"/roles/admin", "", key())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"roles":["admin"]`) {
		t.Errorf("add role: %d %v", rec.Code, out)
	}
	if rec, _ = f.doH(t, "PUT", admin+"/users/"+dan+"/roles/root", "", key()); rec.Code != http.StatusBadRequest {
		t.Errorf("undeclared role: %d", rec.Code)
	}
	rec, _ = f.doH(t, "DELETE", admin+"/users/"+dan+"/roles/admin", "", key())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"roles":[]`) {
		t.Errorf("remove role: %d %s", rec.Code, rec.Body)
	}

	// Delete.
	if rec, _ = f.doH(t, "DELETE", admin+"/users/"+dan, "", key()); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ = f.doH(t, "GET", admin+"/users/"+dan, "", key()); rec.Code != http.StatusNotFound {
		t.Errorf("after delete: %d", rec.Code)
	}
}

func TestInvitationsOverHTTP(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Settings.Signup = "invite"
	keyJSON := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}

	// Without an invitation, sign-up is refused.
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "dan@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "requires an invitation") {
		t.Errorf("signup without invitation: %d %v", rec.Code, out)
	}

	rec, out = f.doH(t, "POST", admin+"/invitations", `{"email": "Dan@example.com", "expires_in": "2d"}`, keyJSON)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(out["token"].(string), "bdi_") || out["email"] != "dan@example.com" || out["created_by"] != "key:svc" {
		t.Fatalf("create invitation: %d %v", rec.Code, out)
	}
	token, id := out["token"].(string), out["id"].(string)

	rec, out = f.doH(t, "GET", admin+"/invitations", "", bearer(f.key))
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 || strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("list invitations: %d %s", rec.Code, rec.Body)
	}

	// Wrong email, then the right one.
	rec, _ = f.do(t, "POST", authBase+"/signup", `{"email": "eve@example.com", "password": "dev-p4ssw0rd!", "invitation": "`+token+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong email: %d", rec.Code)
	}
	rec, out = f.do(t, "POST", authBase+"/signup", `{"email": "dan@example.com", "password": "dev-p4ssw0rd!", "invitation": "`+token+`"}`)
	if rec.Code != http.StatusCreated || out["token"] == nil {
		t.Errorf("signup with invitation: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/invitations/"+id, "", bearer(f.key)); rec.Code != http.StatusNotFound {
		t.Errorf("revoke used invitation: %d", rec.Code)
	}

	// Revoke an unused one.
	_, out = f.doH(t, "POST", admin+"/invitations", `{}`, keyJSON)
	if rec, _ := f.doH(t, "DELETE", admin+"/invitations/"+out["id"].(string), "", bearer(f.key)); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	rec, _ = f.do(t, "POST", authBase+"/signup", `{"email": "eve@example.com", "password": "dev-p4ssw0rd!", "invitation": "`+out["token"].(string)+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("revoked invitation used: %d", rec.Code)
	}

	for body, path := range map[string]string{`{"expires_in": "soon"}`: "expires_in", `{"expires_in": "365d"}`: "expires_in", `{"email": "nope"}`: "email", `{"role": "x"}`: "role"} {
		rec, _ := f.doH(t, "POST", admin+"/invitations", body, keyJSON)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"path":"`+path+`"`) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// Sessions can't create invitations.
	if rec, _ := f.doH(t, "POST", admin+"/invitations", `{}`, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Errorf("session creates invitation: %d", rec.Code)
	}
}

func TestAdminAPIKeysAndNetworks(t *testing.T) {
	f := newRulesFixture(t)
	h := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}
	rec, out := f.doH(t, "POST", admin+"/apikeys", `{"name": "billing", "networks": ["192.0.2.0/24", "2001:db8::1"], "expires_in": "30d"}`, h)
	key, _ := out["key"].(string)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(key, "bdk_") || out["role"] != "data" ||
		!reflect.DeepEqual(out["networks"], []any{"192.0.2.0/24", "2001:db8::1/128"}) || out["expires_at"] == nil {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	// The new key works from its network (the test client is 192.0.2.1).
	if rec, _ := f.doH(t, "GET", posts, "", bearer(key)); rec.Code != http.StatusOK {
		t.Errorf("new key: %d", rec.Code)
	}
	rec, out = f.doH(t, "GET", admin+"/apikeys", "", h)
	items, _ := out["items"].([]any)
	found := false
	for _, it := range items {
		k := it.(map[string]any)
		if k["name"] == "billing" {
			found = true
			if _, leaked := k["key"]; leaked {
				t.Error("list shows the key")
			}
		}
	}
	if rec.Code != http.StatusOK || !found {
		t.Errorf("list: %d %v", rec.Code, out)
	}
	for _, tt := range []struct{ body, path string }{
		{`{"name": "Bad Name"}`, "name"},
		{`{"name": "x", "networks": ["office"]}`, "networks"},
		{`{"name": "x", "networks": "10.0.0.0/8"}`, "networks"},
		{`{"name": "x", "expires_in": "soon"}`, "expires_in"},
		{`{"name": "x", "extra": 1}`, "extra"},
	} {
		rec, out := f.doH(t, "POST", admin+"/apikeys", tt.body, h)
		d, _ := out["error"].(map[string]any)["details"].([]any)
		if rec.Code != http.StatusBadRequest || len(d) == 0 || d[0].(map[string]any)["path"] != tt.path {
			t.Errorf("%s: %d %v", tt.body, rec.Code, out)
		}
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/apikeys/billing", "", h); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", posts, "", bearer(key)); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key still works: %d", rec.Code)
	}

	// A user's networks: set, shown, enforced, cleared.
	nets := admin + "/users/" + f.bobID + "/networks"
	rec, out = f.doH(t, "PUT", nets, `{"admin_networks": [], "login_networks": ["198.51.100.0/24"]}`, h)
	if rec.Code != http.StatusOK || !reflect.DeepEqual(out["login_networks"], []any{"198.51.100.0/24"}) || !reflect.DeepEqual(out["admin_networks"], []any{}) {
		t.Fatalf("set networks: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", posts, "", bearer(f.bob)); rec.Code != http.StatusUnauthorized {
		t.Errorf("bob's session from outside his login networks: %d", rec.Code)
	}
	if rec, out := f.doH(t, "PUT", nets, `{"admin_networks": [], "login_networks": []}`, h); rec.Code != http.StatusOK || !reflect.DeepEqual(out["login_networks"], []any{}) {
		t.Errorf("clear networks: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", posts, "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("bob's session after clearing: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "PUT", nets, `{"admin_networks": []}`, h); rec.Code != http.StatusBadRequest {
		t.Errorf("missing login_networks: %d", rec.Code)
	}
}
