package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// TestOnBehalfOfIsRefusedOnAccountAndAdminRoutes: X-Backd-On-Behalf-Of is for data, files,
// functions and organizations; the routes that act on a person's own account and the admin API
// answer 400 invalid_header (design/act-as-user.md §3).
func TestOnBehalfOfIsRefusedOnAccountAndAdminRoutes(t *testing.T) {
	f := newAuthFixture(t)
	token, out := f.signup(t, "ada@example.com")
	adaID := out["user"].(map[string]any)["id"].(string)
	_, key, err := f.svc.CreateAPIKey(context.Background(), "svc", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	behalf := map[string]string{"Authorization": "Bearer " + key, "X-Backd-On-Behalf-Of": adaID}

	for _, rt := range []struct{ method, path, body string }{
		{"GET", authBase + "/me", ""},
		{"GET", authBase + "/sessions", ""},
		{"POST", authBase + "/logout", ""},
		{"POST", authBase + "/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`},
		{"POST", authBase + "/mfa/totp/setup", ""},
		{"GET", "/v1/acme/_admin/users", ""},
	} {
		rec, got := f.doH(t, rt.method, rt.path, rt.body, behalf)
		if rec.Code != http.StatusBadRequest || errCode(got) != "invalid_header" {
			t.Errorf("%s %s with the header: %d %v", rt.method, rt.path, rec.Code, got)
		}
		if strings.HasPrefix(rt.path, authBase) && rt.method == "GET" {
			// The same route without the header is not refused for that reason.
			rec, got = f.doH(t, rt.method, rt.path, rt.body, bearer(token))
			if errCode(got) == "invalid_header" {
				t.Errorf("%s %s without the header: %d %v", rt.method, rt.path, rec.Code, got)
			}
		}
	}
}

// A key cannot act as an administrator, on any route: acting as a user must never bring more
// privileges than the key has.
func TestAKeyCannotActAsAnAdministratorOnTheDataRoutes(t *testing.T) {
	f := newRulesFixture(t)
	_, plain, err := f.svc.CreateAPIKey(context.Background(), "service", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	as := func(key, user string) (int, string) {
		rec, out := f.doH(t, "GET", "/v1/acme/app/notes", "", map[string]string{"Authorization": "Bearer " + key, "X-Backd-On-Behalf-Of": user})
		e, _ := out["error"].(map[string]any)
		msg, _ := e["message"].(string)
		return rec.Code, msg
	}
	// "staff" holds admin rights in this realm, "admin" is only a name.
	if err := f.svc.AddRole(context.Background(), "ada@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"an admin key": f.key, "a data key": plain} {
		if code, msg := as(key, f.adaID); code != http.StatusForbidden || !strings.Contains(msg, "cannot act as an administrator") {
			t.Errorf("%s acting as an administrator: %d %q", name, code, msg)
		}
		if code, _ := as(key, f.bobID); code == http.StatusForbidden || code == http.StatusBadRequest {
			t.Errorf("%s acting as a user without admin rights: %d", name, code)
		}
	}
	// A role that only has a name is not an administrator.
	if err := f.svc.AddRole(context.Background(), "bob@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	if code, _ := as(plain, f.bobID); code == http.StatusForbidden || code == http.StatusBadRequest {
		t.Errorf("acting as a user whose role has no admin rights: %d", code)
	}
}
