package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAdminExpireEveryPassword(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	_, adminKey, err := f.svc.CreateAPIKey(context.Background(), "breach", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	url := admin + "/passwords/expire"

	// Rights: a read-only administrator is refused, a session needs a fresh proof.
	f.bobHolds(t, "viewer")
	if rec, _ := f.doH(t, "POST", url, `{}`, f.withProof(t, f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only administrator: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/sessions", "", f.withProof(t, f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only administrator, sessions: %d", rec.Code)
	}
	f.bobHolds(t, "staff")
	rec, out := f.doH(t, "POST", url, `{}`, jsonHdr(f.bob))
	if rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("a session without proof: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", url, `{}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", rec.Code)
	}
	if rec, out := f.doH(t, "POST", url, `{"reason": 7}`, key); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad reason: %d %v", rec.Code, out)
	}

	// Revoking every session leaves passwords alone.
	rec, out = f.doH(t, "DELETE", admin+"/sessions", "", bearer(adminKey))
	if rec.Code != http.StatusOK || out["sessions"].(float64) < 1 {
		t.Fatalf("revoke sessions: %d %v", rec.Code, out)
	}
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/_auth/me", ""); code != http.StatusUnauthorized {
		t.Errorf("a session after revoke-all: %d", code)
	}
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusOK {
		t.Errorf("sign in after revoke-all: %d", rec.Code)
	}

	// The bulk expiry: the answer counts, passwords stop working, sessions end.
	at := time.Now().Add(time.Hour)
	f.svc.Now = func() time.Time { return at }
	rec, out = f.doH(t, "POST", url, `{"reason": "leaked backup"}`, key)
	if rec.Code != http.StatusOK || out["accounts"].(float64) < 2 || out["valid_after"] == "" {
		t.Fatalf("expire: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnauthorized || errCode(out) != "password_expired" {
		t.Errorf("sign in after the epoch: %d %v", rec.Code, out)
	}
	// An epoch moves forward only: the same instant is refused.
	if rec, out := f.doH(t, "POST", url, `{}`, key); rec.Code != http.StatusConflict {
		t.Errorf("the same instant: %d %v", rec.Code, out)
	}
}

func TestAdminExpirePasswordsAndTellTheUsers(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	_, adminKey, err := f.svc.CreateAPIKey(context.Background(), "breach", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	login := func() int {
		rec, _ := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, map[string]string{"Content-Type": "application/json"})
		return rec.Code
	}

	// Without email, notify is refused before anything changes.
	settings := f.svc.Settings
	f.svc.Settings.Email = nil
	rec, out := f.doH(t, "POST", admin+"/passwords/expire", `{"notify": true}`, key)
	if rec.Code != http.StatusConflict || !strings.Contains(out["error"].(map[string]any)["message"].(string), "reset-link") {
		t.Errorf("notify without email: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", admin+"/passwords/notify", ``, key); rec.Code != http.StatusConflict {
		t.Errorf("notify route without email: %d", rec.Code)
	}
	if login() != http.StatusOK {
		t.Error("the refused request expired passwords anyway")
	}
	f.svc.Settings = settings

	// Before any expiry there is nobody to tell.
	if rec, _ := f.doH(t, "POST", admin+"/passwords/notify", ``, key); rec.Code != http.StatusConflict {
		t.Errorf("notify before an expiry: %d", rec.Code)
	}
	at := time.Now().Add(time.Hour)
	f.svc.Now = func() time.Time { return at }
	rec, out = f.doH(t, "POST", admin+"/passwords/expire", `{"notify": true, "reason": "drill"}`, key)
	job, _ := out["notify_job"].(string)
	if rec.Code != http.StatusOK || job == "" {
		t.Fatalf("expire and notify: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", admin+"/jobs/"+job, "", key); rec.Code != http.StatusOK || out["origin"] != "backd:password.notify" || out["function"] != "password-notice" {
		t.Errorf("the job: %d %v", rec.Code, out)
	}
}
