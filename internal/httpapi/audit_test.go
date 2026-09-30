package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// Every audited action through the API, then two checks on the trail: each
// action is recorded with who, what and from where, and no record holds a
// secret or an email (roadmap S3).
func TestAuditTrail(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		"acme/realm.yaml": `signup: invite
admin:
  allowed_networks: [192.0.2.0/24]
roles:
  ops:
    admin: true
    users: [ada@example.com]
  editor: {}
`,
		"acme/app/notes/schema.json": `{}`,
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	store := authtest.NewMemStore()
	svc := &auth.Users{
		Store:    store,
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
		Realm:    "acme",
	}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		}
	})
	ctx := context.Background()
	const adaPassword = "ada's long password"
	if _, err := svc.Create(ctx, "ada@example.com", ptr(adaPassword)); err != nil {
		t.Fatal(err)
	}

	secrets := []string{adaPassword}
	call := func(ip, method, path, body, token string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = ip + ":40000"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	const in, out = "192.0.2.10", "198.51.100.7"
	const base = "/v1/acme/"

	login := call(in, "POST", base+"_auth/login", `{"email": "ada@example.com", "password": "`+adaPassword+`"}`, "", 200)
	ada := login["token"].(string)
	adaID := login["user"].(map[string]any)["id"].(string)
	secrets = append(secrets, ada, auth.HashToken(ada))

	key := call(in, "POST", base+"_admin/apikeys", `{"name": "tooling", "role": "admin", "expires_in": "1d", "networks": ["192.0.2.0/24"]}`, ada, 201)["key"].(string)
	secrets = append(secrets, key, auth.HashToken(key))

	const bobPassword = "bob's first password"
	bob := call(in, "POST", base+"_admin/users", `{"email": "bob@example.com", "password": "`+bobPassword+`"}`, key, 201)["id"].(string)
	u := base + "_admin/users/" + bob
	call(in, "PATCH", u, `{"email_verified": true}`, key, 200)
	call(in, "PATCH", u, `{"disabled": true}`, key, 200)
	call(in, "PATCH", u, `{"disabled": false}`, key, 200)
	const bobPassword2 = "bob's second password"
	call(in, "POST", u+"/password", `{"password": "`+bobPassword2+`"}`, ada, 204)
	call(in, "PUT", u+"/roles/editor", "", ada, 200)
	call(in, "DELETE", u+"/roles/editor", "", ada, 200)
	call(in, "PUT", u+"/networks", `{"admin_networks": [], "login_networks": ["192.0.2.0/24"]}`, ada, 200)
	inv := call(in, "POST", base+"_admin/invitations", `{"email": "cy@example.com"}`, ada, 201)
	invToken, invID := inv["token"].(string), inv["id"].(string)
	inv2 := call(in, "POST", base+"_admin/invitations", `{}`, ada, 201)
	call(in, "DELETE", base+"_admin/invitations/"+inv2["id"].(string), "", ada, 204)
	secrets = append(secrets, bobPassword, bobPassword2, invToken, auth.HashToken(invToken), inv2["token"].(string))

	const cyPassword, cyPassword2 = "cy's own password", "cy's changed password"
	cyUp := call(in, "POST", base+"_auth/signup", `{"email": "cy@example.com", "password": "`+cyPassword+`", "invitation": "`+invToken+`"}`, "", 201)
	cy, cyID := cyUp["token"].(string), cyUp["user"].(map[string]any)["id"].(string)
	call(in, "POST", base+"_auth/password", `{"current_password": "`+cyPassword+`", "new_password": "`+cyPassword2+`"}`, cy, 204)
	call(in, "DELETE", base+"_auth/me", `{"password": "`+cyPassword2+`"}`, cy, 204)
	secrets = append(secrets, cyPassword, cyPassword2, cy)

	call(in, "DELETE", u, "", ada, 204)
	call(in, "DELETE", base+"_admin/apikeys/tooling", "", ada, 204)
	call(out, "GET", base+"_admin/users", "", ada, 404) // refused by network

	// Seeds from realm.yaml, as at startup.
	svc.Settings.Roles["editor"] = registry.Role{Users: []string{"ada@example.com"}}
	if _, err := svc.ApplyRoleSeeds(ctx); err != nil {
		t.Fatal(err)
	}

	recs := store.AuditRecords()
	find := func(action, target string) auth.AuditRecord {
		t.Helper()
		for _, r := range recs {
			if r.Action == action && (target == "" || r.Target == target) {
				return r
			}
		}
		t.Errorf("no %s record for %q", action, target)
		return auth.AuditRecord{}
	}
	ku, adaU, cyU, bobU := "key:tooling", "user:"+adaID, "user:"+cyID, "user:"+bob
	for _, w := range []struct{ action, target, actor string }{
		{auth.AuditAdminLogin, adaU, adaU},
		{auth.AuditAPIKeyCreate, ku, adaU},
		{auth.AuditUserCreate, bobU, ku},
		{auth.AuditUserVerifyEmail, bobU, ku},
		{auth.AuditUserDisable, bobU, ku},
		{auth.AuditUserEnable, bobU, ku},
		{auth.AuditUserPassword, bobU, adaU},
		{auth.AuditRoleAdd, bobU, adaU},
		{auth.AuditRoleRemove, bobU, adaU},
		{auth.AuditUserNetworks, bobU, adaU},
		{auth.AuditInviteCreate, "invitation:" + invID, adaU},
		{auth.AuditInviteRevoke, "invitation:" + inv2["id"].(string), adaU},
		{auth.AuditUserSignup, cyU, cyU},
		{auth.AuditPasswordChange, cyU, cyU},
		{auth.AuditAccountDelete, cyU, cyU},
		{auth.AuditUserDelete, bobU, adaU},
		{auth.AuditAPIKeyRevoke, ku, adaU},
		{auth.AuditAdminRefused, "", auth.ActorAnonymous},
		{auth.AuditRoleAdd, adaU, auth.ActorConfig},
	} {
		r := find(w.action, w.target)
		if r.Actor != w.actor {
			t.Errorf("%s %s: actor %q, want %q", w.action, w.target, r.Actor, w.actor)
		}
		if w.actor != auth.ActorConfig && (r.RequestID == "" || !strings.HasPrefix(r.ClientIP, "19")) {
			t.Errorf("%s: request id %q, client %q", w.action, r.RequestID, r.ClientIP)
		}
		if r.ExpiresAt.Sub(r.At) != registry.DefaultAuditRetention {
			t.Errorf("%s: kept for %s", w.action, r.ExpiresAt.Sub(r.At))
		}
	}
	if d := find(auth.AuditAPIKeyCreate, ku).Details; d["role"] != "admin" || !slices.Equal(d["networks"].([]string), []string{"192.0.2.0/24"}) || d["expires_at"] == nil {
		t.Errorf("key details: %v", d)
	}
	if d := find(auth.AuditAdminRefused, "").Details; d["reason"] != "outside the user's admin_networks" && d["reason"] != "outside the realm's admin.allowed_networks" {
		t.Errorf("refusal details: %v", d)
	}

	// No secrets and no emails, anywhere in any record.
	all, _ := json.Marshal(recs)
	for _, s := range append(secrets, "ada@example.com", "bob@example.com", "cy@example.com", "$argon2id", "bds_", "bdk_", "bdi_") {
		if strings.Contains(string(all), s) {
			t.Errorf("audit trail contains %q", s)
		}
	}

	// The API reads the trail, filtered, newest first; nothing can change it.
	page := call(in, "GET", base+"_admin/audit?target="+bobU+"&limit=3", "", ada, 200)
	items := page["items"].([]any)
	if len(items) != 3 || page["has_more"] != true || items[0].(map[string]any)["action"] != auth.AuditUserDelete {
		t.Errorf("audit page: %v", page)
	}
	call(in, "DELETE", base+"_admin/audit", "", ada, 405)
}

func ptr[T any](v T) *T { return &v }
