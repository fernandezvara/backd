package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (f *rulesFixture) resetRequest(t *testing.T, address string) (int, string) {
	t.Helper()
	rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", `{"email": "`+address+`"}`, "")
	return rec.Code, rec.Body.String()
}

// The whole flow: request, email, form, new password; no session is issued,
// every session ends, the address becomes verified and the owner is told.
func TestPasswordResetFlow(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	const newPassword = "dev-p4ssw0rd!2"

	if code, _ := f.resetRequest(t, "Carl@Example.com"); code != 202 { // carl never verified his address
		t.Fatalf("request: %d", code)
	}
	in := deliver(t, f, w)
	if in.Kind != "reset-password" || in.To[0].Email != "carl@example.com" {
		t.Fatalf("message: %+v", in)
	}
	token := tokenOf(t, in)
	if want := f.clock.Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z07:00"); in.Data["expires_at"] != want {
		t.Errorf("expires_at %v, want %s", in.Data["expires_at"], want)
	}
	if rec := f.page(t, "GET", "/v1/acme/_auth/reset-password?token="+token, "", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="password_confirm"`) {
		t.Fatalf("form: %d %s", rec.Code, rec.Body)
	}
	post := func(password, confirm string) (int, string) {
		rec := f.page(t, "POST", "/v1/acme/_auth/reset-password", formType, url.Values{"token": {token}, "password": {password}, "password_confirm": {confirm}}.Encode(), "")
		return rec.Code, rec.Body.String()
	}
	if code, body := post("short", "short"); code != 400 || !strings.Contains(body, "at least 12 characters") {
		t.Errorf("policy: %d", code)
	}
	if code, _ := post(newPassword, "other"); code != 400 {
		t.Errorf("mismatch: %d", code)
	}
	// A refused password left the token usable.
	if code, body := post(newPassword, newPassword); code != 200 || strings.Contains(body, "bds_") {
		t.Fatalf("reset: %d %s", code, body)
	}
	if code, _ := post(newPassword, newPassword); code != 400 {
		t.Errorf("a used link: %d", code)
	}

	login := func(password string) int {
		return f.page(t, "POST", "/v1/acme/_auth/login", "application/json", `{"email": "carl@example.com", "password": "`+password+`"}`, "").Code
	}
	if login("dev-p4ssw0rd!") != 401 || login(newPassword) != 200 {
		t.Error("the old password still works, or the new one doesn't")
	}
	if code, _ := f.as(t, f.carl, "GET", "/v1/acme/_auth/me", ""); code != 401 {
		t.Errorf("a session from before the reset: %d", code)
	}
	u, _ := f.svc.Find(ctx, "carl@example.com")
	if !u.EmailVerified {
		t.Error("redeeming the token should verify the address")
	}
	if got := deliver(t, f, w); got.Kind != "password-changed" || got.To[0].Email != "carl@example.com" {
		t.Errorf("notification: %+v", got)
	}
}

func TestPasswordResetJSONAndLifetime(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Account.ResetPasswordTTL = 30 * time.Minute
	f.resetRequest(t, "ada@example.com")
	in := deliver(t, f, w)
	token := tokenOf(t, in)
	if want := f.clock.Add(30 * time.Minute).UTC().Format(time.RFC3339); in.Data["expires_at"] != want {
		t.Errorf("expires_at %v, want %s", in.Data["expires_at"], want)
	}
	*f.clock = f.clock.Add(time.Hour)
	body := `{"token": "` + token + `", "password": "dev-p4ssw0rd!2"}`
	if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password", "application/json", body, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_token") {
		t.Errorf("expired: %d %s", rec.Code, rec.Body)
	}
}

// The request answers the same for every address, and only an enabled
// account gets a message; the limits count for unknown addresses too.
func TestPasswordResetRequestIsUniform(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	if err := f.svc.SetDisabled(ctx, "bob@example.com", true); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, addr := range []string{"ada@example.com", "bob@example.com", "nobody@example.com"} { // known, disabled, unknown
		code, body := f.resetRequest(t, addr)
		if code != 202 {
			t.Fatalf("%s: %d %s", addr, code, body)
		}
		bodies = append(bodies, body)
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ: %q", bodies)
	}
	if in := deliver(t, f, w); in.To[0].Email != "ada@example.com" {
		t.Errorf("sent to %s", in.To[0].Email)
	}
	if w.RunOnce(ctx) {
		t.Error("an email was sent for a disabled or unknown address")
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", `{"email": "ada@example.com", "redirect_to": "https://evil.example/"}`, ""); rec.Code != 400 {
		t.Errorf("redirect: %d", rec.Code)
	}
	// Past the limit, a known and an unknown address are both answered 429.
	for _, addr := range []string{"ada@example.com", "nobody@example.com"} {
		var last int
		for range 6 {
			last, _ = f.resetRequest(t, addr)
		}
		if last != 429 {
			t.Errorf("%s: sixth request %d, want 429", addr, last)
		}
	}
}

// Whoever changes a password hears about it.
func TestPasswordChangedEmail(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	code, _ := f.as(t, f.bob, "POST", "/v1/acme/_auth/password", `{"current_password": "dev-p4ssw0rd!", "new_password": "dev-p4ssw0rd!2"}`)
	if code != 204 {
		t.Fatalf("change: %d", code)
	}
	if in := deliver(t, f, w); in.Kind != "password-changed" || in.To[0].Email != "bob@example.com" {
		t.Errorf("after a change: %+v", in)
	}
	if err := f.svc.SetPassword(ctx, "ada@example.com", "dev-p4ssw0rd!3"); err != nil {
		t.Fatal(err)
	}
	if in := deliver(t, f, w); in.Kind != "password-changed" || in.To[0].Email != "ada@example.com" {
		t.Errorf("after an admin set it: %+v", in)
	}
}
