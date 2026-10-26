package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAdminExpireEveryPassword(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Settings.Organizations = nil // plain sign-in: a realm without organizations
	f.svc.Realm = "acme"
	_, adminKey, err := f.svc.CreateAPIKey(context.Background(), "breach", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	url := admin + "/passwords/expire"
	if _, out := f.doH(t, "GET", admin+"/passwords", "", key); out["epoch"] != nil {
		t.Errorf("an epoch before any expiry: %v", out)
	}

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
	// The state before and the detail of a user show the expiry.
	rec, out = f.doH(t, "GET", admin+"/passwords", "", key)
	epoch, _ := out["epoch"].(map[string]any)
	if rec.Code != http.StatusOK || out["accounts"].(float64) < 2 || epoch["reason"] != "leaked backup" || epoch["valid_after"] == "" {
		t.Errorf("state: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", admin+"/users/"+f.adaID, "", key); rec.Code != http.StatusOK || out["password_expired"] != true {
		t.Errorf("a user's detail: %d %v", rec.Code, out)
	}
	// An epoch moves forward only: the same instant is refused.
	if rec, out := f.doH(t, "POST", url, `{}`, key); rec.Code != http.StatusConflict {
		t.Errorf("the same instant: %d %v", rec.Code, out)
	}
}

func TestAdminExpirePasswordsAndTellTheUsers(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Settings.Organizations = nil // plain sign-in: a realm without organizations
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

func TestAdminResetLink(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Settings.Organizations = nil // plain sign-in: a realm without organizations
	f.svc.Realm = "acme"
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "support", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	url := admin + "/users/" + f.adaID + "/reset-link"

	// Rights: read-only administrators are refused, a session needs a fresh proof.
	f.bobHolds(t, "viewer")
	if rec, _ := f.doH(t, "POST", url, `{}`, f.withProof(t, f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only administrator: %d", rec.Code)
	}
	f.bobHolds(t, "staff")
	if rec, out := f.doH(t, "POST", url, `{}`, jsonHdr(f.bob)); rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("a session without proof: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", url, `{}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", rec.Code)
	}
	if rec, out := f.doH(t, "POST", url, `{"redirect_to": "https://evil.example/"}`, key); rec.Code != http.StatusBadRequest || errCode(out) != "invalid_redirect" {
		t.Errorf("a redirect outside the allowed ones: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", admin+"/users/nope/reset-link", ``, key); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown user: %d", rec.Code)
	}

	rec, out := f.doH(t, "POST", url, ``, key)
	link, _ := out["link"].(string)
	_, token, found := strings.Cut(link, "/v1/acme/_auth/reset-password?token=")
	if rec.Code != http.StatusOK || !found || token == "" || out["expires_at"] == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("the link: %d %v", rec.Code, out)
	}
	// Nothing was sent: no reset email is queued, and the audit record names the user, not the token.
	jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Limit: 100})
	for _, j := range jobs {
		if j.Email != nil && j.Email.Kind == "reset-password" {
			t.Error("making a link queued an email")
		}
	}
	trail, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditPasswordResetLink, Limit: 5})
	if len(trail) != 1 || trail[0].Target != "user:"+f.adaID {
		t.Fatalf("audit: %+v", trail)
	}
	if b, _ := json.Marshal(trail[0]); strings.Contains(string(b), token) {
		t.Error("the audit record holds the token")
	}

	// Using it works as a reset does: the password changes, the sessions end, the link is used up.
	rec, out = f.doH(t, "POST", "/v1/acme/_auth/reset-password", `{"token": "`+token+`", "password": "dev-p4ssw0rd!9"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("use the link: %d %v", rec.Code, out)
	}
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/_auth/me", ""); code != http.StatusUnauthorized {
		t.Errorf("a session after the reset: %d", code)
	}
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!9"}`, map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusOK {
		t.Errorf("sign in with the new password: %d", rec.Code)
	}
	rec, out = f.doH(t, "POST", "/v1/acme/_auth/reset-password", `{"token": "`+token+`", "password": "dev-p4ssw0rd!8"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest || errCode(out) != "invalid_token" {
		t.Errorf("the link twice: %d %v", rec.Code, out)
	}
	jobs, _, _ = f.svc.Jobs(ctx, auth.JobFilter{Limit: 100})
	told := false
	for _, j := range jobs {
		told = told || (j.Email != nil && j.Email.Kind == "password-changed")
	}
	if !told {
		t.Error("the user wasn't told that the password changed")
	}

	// An unused link expires like a reset token.
	_, out = f.doH(t, "POST", url, ``, key)
	_, stale, _ := strings.Cut(out["link"].(string), "?token=")
	f.svc.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	rec, out = f.doH(t, "POST", "/v1/acme/_auth/reset-password", `{"token": "`+stale+`", "password": "dev-p4ssw0rd!7"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest || errCode(out) != "invalid_token" {
		t.Errorf("an expired link: %d %v", rec.Code, out)
	}
	f.svc.Now = nil

	// A disabled user gets no link.
	if err := f.svc.SetDisabled(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "POST", url, ``, key); rec.Code != http.StatusConflict {
		t.Errorf("a disabled user: %d", rec.Code)
	}
}
