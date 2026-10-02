package httpapi

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/executor"
)

const signupBody = `{"email": "new@example.com", "password": "dev-p4ssw0rd!", "redirect_to": "https://app.acme.example/welcome"}`

// deliver runs the worker once and returns the message the delivery function got.
func deliver(t *testing.T, f *rulesFixture, w *Worker) emailInput {
	t.Helper()
	if !w.RunOnce(context.Background()) {
		t.Fatal("no job to run")
	}
	return deliveredEmail(t, f.runner.last())
}

func newVerifyFixture(t *testing.T) (*rulesFixture, *Worker) {
	t.Helper()
	f := newHostedFixture(t)
	w := newTestWorker(t, f)
	w.reg = f.reg
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{}`)}, nil
	})
	return f, w
}

// Sign-up sends the verification email; its link verifies the address, uses
// the stored redirect and never gives a session.
func TestVerifyEmailFlow(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Account.WelcomeEmail = true
	ctx := context.Background()

	rec := f.page(t, "POST", "/v1/acme/_auth/signup", "application/json", signupBody, "es")
	if rec.Code != 201 {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	in := deliver(t, f, w)
	if in.Kind != "verify-email" || in.Locale != "es" || in.To[0].Email != "new@example.com" {
		t.Fatalf("message: %+v", in)
	}
	token := tokenOf(t, in)
	u, _ := f.svc.Find(ctx, "new@example.com")
	if u.EmailVerified {
		t.Fatal("a self sign-up starts unverified")
	}

	// Opening the link (a scanner) changes nothing; posting the page does.
	f.page(t, "GET", "/v1/acme/_auth/verify-email?token="+token, "", "", "")
	if u, _ = f.svc.Find(ctx, "new@example.com"); u.EmailVerified {
		t.Fatal("GET verified the address")
	}
	rec = f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "url=https://app.acme.example/welcome") {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	if u, _ = f.svc.Find(ctx, "new@example.com"); !u.EmailVerified {
		t.Error("the address isn't verified")
	}
	if rec.Header().Get("Authorization") != "" || strings.Contains(rec.Body.String(), "bds_") {
		t.Error("verification issued a session")
	}
	// The same link again, and the welcome email.
	if rec = f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), ""); rec.Code != 400 {
		t.Errorf("a used link: %d", rec.Code)
	}
	if in = deliver(t, f, w); in.Kind != "welcome" || in.To[0].Email != "new@example.com" {
		t.Errorf("welcome: %+v", in)
	}
	if w.RunOnce(ctx) {
		t.Error("an extra email was queued")
	}
}

func TestVerifyEmailTokenLifetime(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Account.VerifyEmailTTL = 2 * time.Hour
	f.page(t, "POST", "/v1/acme/_auth/signup", "application/json", signupBody, "")
	in := deliver(t, f, w)
	if want := f.clock.Add(2 * time.Hour).UTC().Format(time.RFC3339); in.Data["expires_at"] != want {
		t.Errorf("expires_at %v, want %s", in.Data["expires_at"], want)
	}
	// Past it, the link is the error page.
	*f.clock = f.clock.Add(3 * time.Hour)
	if rec := f.page(t, "GET", "/v1/acme/_auth/verify-email?token="+tokenOf(t, in), "", "", ""); rec.Code != 400 {
		t.Errorf("an expired link: %d", rec.Code)
	}
}

// With require_verified_email there is no session until the address is
// verified, and the gate only speaks to someone who knows the password.
func TestRequireVerifiedEmail(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Account.RequireVerifiedEmail = true

	rec := f.page(t, "POST", "/v1/acme/_auth/signup", "application/json", signupBody, "")
	if rec.Code != 202 || strings.Contains(rec.Body.String(), "token") || !strings.Contains(rec.Body.String(), "verification_required") {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	login := func(password string) int {
		return f.page(t, "POST", "/v1/acme/_auth/login", "application/json", `{"email": "new@example.com", "password": "`+password+`"}`, "").Code
	}
	if got := login("dev-p4ssw0rd!"); got != 403 {
		t.Errorf("right password, unverified: %d", got)
	}
	if got := login("dev-p4ssw0rd!0"); got != 401 {
		t.Errorf("wrong password: %d", got)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/login", "application/json", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`, ""); !strings.Contains(rec.Body.String(), "email_not_verified") {
		t.Errorf("code: %s", rec.Body)
	}

	token := tokenOf(t, deliver(t, f, w))
	if rec := f.page(t, "POST", "/v1/acme/_auth/verify-email", "application/json", `{"token": "`+token+`"}`, ""); rec.Code != 204 {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	if got := login("dev-p4ssw0rd!"); got != 200 {
		t.Errorf("after verifying: %d", got)
	}

	// An invitation bound to the address was sent to it: no verification needed.
	f.svc.Settings.Signup = "invite"
	inv, tok, err := f.svc.CreateInvitation(context.Background(), "invited@example.com", 0, "test")
	if err != nil || inv.Email == "" {
		t.Fatal(err)
	}
	body := `{"email": "invited@example.com", "password": "dev-p4ssw0rd!", "invitation": "` + tok + `"}`
	if rec := f.page(t, "POST", "/v1/acme/_auth/signup", "application/json", body, ""); rec.Code != 201 {
		t.Errorf("a bound invitation: %d %s", rec.Code, rec.Body)
	}
	if w.RunOnce(context.Background()) {
		t.Error("a verified user was sent a verification email")
	}
}

// Resend answers the same for every account, and only an unverified one gets
// a message.
func TestResendVerification(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	f.page(t, "POST", "/v1/acme/_auth/signup", "application/json", signupBody, "")
	deliver(t, f, w)

	var bodies []string
	for _, addr := range []string{"new@example.com", "ada@example.com", "nobody@example.com"} { // unverified, verified, unknown
		rec := f.page(t, "POST", "/v1/acme/_auth/verify-email/resend", "application/json", `{"email": "`+addr+`"}`, "")
		if rec.Code != 202 {
			t.Fatalf("resend %s: %d %s", addr, rec.Code, rec.Body)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ: %q", bodies)
	}
	in := deliver(t, f, w)
	if in.To[0].Email != "new@example.com" {
		t.Errorf("sent to %s", in.To[0].Email)
	}
	if w.RunOnce(ctx) {
		t.Error("an email was sent for a verified or unknown address")
	}
	// A disallowed redirect is refused for every address alike.
	for _, addr := range []string{"new@example.com", "nobody@example.com"} {
		rec := f.page(t, "POST", "/v1/acme/_auth/verify-email/resend", "application/json", `{"email": "`+addr+`", "redirect_to": "https://evil.example/"}`, "")
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_redirect") {
			t.Errorf("redirect for %s: %d %s", addr, rec.Code, rec.Body)
		}
	}
	// The limits count the same for known and unknown addresses (3 per kind
	// and hour; two were used for new@ above, counting the sign-up).
	store := f.svc.Store.(*authtest.MemStore)
	keys := 0
	for _, k := range store.CounterKeys() {
		if strings.HasPrefix(k, "email:to:") {
			keys++
		}
	}
	if keys < 3 { // new@, ada@ and nobody@ all counted
		t.Errorf("counters for %d recipients, want 3 (%v)", keys, store.CounterKeys())
	}
}

// Accounts that never verified are purged after the configured time; those
// with roles and the verified ones stay.
func TestPurgeUnverified(t *testing.T) {
	f, _ := newVerifyFixture(t)
	ctx := context.Background()
	f.svc.Settings.Account.PurgeUnverifiedAfter = 24 * time.Hour
	for _, e := range []string{"squat@example.com", "staff@example.com"} {
		if _, _, err := f.svc.Signup(ctx, e, "dev-p4ssw0rd!", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.AddRole(ctx, "staff@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.svc.PurgeUnverified(ctx); err != nil || n != 0 { // too young
		t.Fatalf("young: %d %v", n, err)
	}
	*f.clock = f.clock.Add(25 * time.Hour)
	if n, err := f.svc.PurgeUnverified(ctx); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	for email, want := range map[string]bool{"squat@example.com": false, "staff@example.com": true, "ada@example.com": true} {
		if _, err := f.svc.Find(ctx, email); (err == nil) != want {
			t.Errorf("%s still there = %v, want %v", email, err == nil, want)
		}
	}
	var audited bool
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditUserPurged})
	for _, r := range recs {
		audited = audited || r.Action == auth.AuditUserPurged
	}
	if !audited {
		t.Error("the purge wasn't audited")
	}
}
