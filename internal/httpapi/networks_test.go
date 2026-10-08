package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// Network restrictions (roadmap S2): the realm's admin networks, a user's
// admin and login networks, and an API key's networks.
func TestNetworkRestrictions(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		"net/realm.yaml": `signup: open
admin:
  allowed_networks: [192.0.2.0/24]
roles:
  ops:
    admin: true
    users:
      - email: ada@example.com
        admin_networks: [192.0.2.1]
        login_networks: [192.0.2.0/28]
      - bob@example.com
`,
		"net/app/notes/schema.json":     `{}`,
		"net/app/notes/collection.yaml": rulesSection("read: user != nil\nwrite: user != nil\n"),
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
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["net"].Settings,
	}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(realm string) *auth.Users {
			if realm == "net" {
				return svc
			}
			return nil
		}
	})
	signup := func(email string) string {
		_, tok, err := svc.Signup(ctx, email, "dev-p4ssw0rd!", "")
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	ada, bob := signup("ada@example.com"), signup("bob@example.com")
	if _, err := svc.ApplyRoleSeeds(ctx); err != nil { // roles and networks from realm.yaml
		t.Fatal(err)
	}
	_, adminKey, _ := svc.CreateAPIKey(ctx, "tooling", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	pinned, _ := registry.ParseNetworks([]string{"203.0.113.0/24"})
	_, pinnedKey, _ := svc.CreateAPIKey(ctx, "pinned", auth.KeyOptions{Networks: pinned})

	from := func(ip, method, path, body, token string) (int, string) {
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
		var out struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Error.Code
	}
	const users, notes = "/v1/net/_admin/users", "/v1/net/app/notes"
	for _, tt := range []struct {
		name, ip, method, path, body, token string
		want                                int
		wantCode                            string
	}{
		// The realm's admin networks: outside them, the admin API doesn't exist.
		{"admin key inside the realm's networks", "192.0.2.50", "GET", users, "", adminKey, 200, ""},
		{"admin key outside them", "198.51.100.7", "GET", users, "", adminKey, 404, "not_found"},
		{"no credentials outside them", "198.51.100.7", "GET", users, "", "", 404, "not_found"},
		{"admin session without own networks", "192.0.2.50", "GET", users, "", bob, 200, ""},
		// A user's admin networks narrow the realm's.
		{"ada from her admin network", "192.0.2.1", "GET", users, "", ada, 200, ""},
		{"ada from elsewhere in the realm's networks", "192.0.2.5", "GET", users, "", ada, 404, "not_found"},
		// A user's login networks cover every use of their session.
		{"ada's session on data from her login network", "192.0.2.5", "GET", notes, "", ada, 200, ""},
		{"ada's session on data from elsewhere", "192.0.2.100", "GET", notes, "", ada, 401, "unauthenticated"},
		{"ada's session on /_auth from elsewhere", "192.0.2.100", "GET", "/v1/net/_auth/me", "", ada, 401, "unauthenticated"},
		{"ada logs in from her login network", "192.0.2.2", "POST", "/v1/net/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, "", 200, ""},
		{"ada's right password from elsewhere", "198.51.100.7", "POST", "/v1/net/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, "", 401, "invalid_credentials"},
		{"bob, without login networks, from anywhere", "198.51.100.7", "GET", notes, "", bob, 200, ""},
		// An API key's networks.
		{"pinned key from its network", "203.0.113.9", "GET", notes, "", pinnedKey, 200, ""},
		{"pinned key from elsewhere", "192.0.2.1", "GET", notes, "", pinnedKey, 401, "unauthenticated"},
	} {
		if code, errCode := from(tt.ip, tt.method, tt.path, tt.body, tt.token); code != tt.want || errCode != tt.wantCode {
			t.Errorf("%s: %d %q, want %d %q", tt.name, code, errCode, tt.want, tt.wantCode)
		}
	}

	// Settings made outside realm.yaml: only within the realm's admin
	// networks, and reported as not in config.
	outside, _ := registry.ParseNetworks([]string{"10.0.0.0/8"})
	if _, err := svc.SetNetworks(ctx, "bob@example.com", outside, nil); !errors.Is(err, auth.ErrNetworksOutsideRealm) {
		t.Errorf("admin networks outside the realm's: %v", err)
	}
	inside, _ := registry.ParseNetworks([]string{"192.0.2.64/26"})
	u, err := svc.SetNetworks(ctx, "bob@example.com", inside, inside)
	if err != nil || !u.LoginNetworks.Equal(inside) {
		t.Fatalf("SetNetworks: %+v %v", u, err)
	}
	if svc.NetworksInConfig("bob@example.com") || !svc.NetworksInConfig("ada@example.com") {
		t.Error("NetworksInConfig")
	}
	rep, err := svc.ReportRoles(ctx)
	if err != nil || len(rep.NetworksDBOnly) != 1 || rep.NetworksDBOnly[0] != "bob@example.com" {
		t.Errorf("NetworksDBOnly = %v, %v", rep.NetworksDBOnly, err)
	}
	if code, _ := from("198.51.100.7", "GET", notes, "", bob); code != http.StatusUnauthorized {
		t.Errorf("bob's session outside his new login networks: %d", code)
	}
	// realm.yaml's settings win at the next seeding.
	if _, err := svc.SetNetworks(ctx, "ada@example.com", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyRoleSeeds(ctx); err != nil {
		t.Fatal(err)
	}
	if code, _ := from("192.0.2.100", "GET", notes, "", ada); code != http.StatusUnauthorized {
		t.Errorf("seeding didn't restore ada's login networks: %d", code)
	}
}
