package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

const changeBody = `{"new_email": "Ada.New@Example.com", "password": "dev-p4ssw0rd!", "redirect_to": "https://app.acme.example/account"}`

func newChangeFixture(t *testing.T) (*rulesFixture, *Worker) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Account.AllowEmailChange = true
	return f, w
}

// The whole flow: request, confirmation at the new address, notice with an
// undo link at the old one, and the undo.
func TestEmailChangeFlowAndRevert(t *testing.T) {
	f, w := newChangeFixture(t)
	ctx := context.Background()
	login := func(address, password string) int {
		return f.page(t, "POST", "/v1/acme/_auth/login", "application/json", `{"email": "`+address+`", "password": "`+password+`"}`, "").Code
	}

	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/_auth/email", changeBody); code != 202 {
		t.Fatalf("request: %d", code)
	}
	in := deliver(t, f, w)
	if in.Kind != "change-email" || in.To[0].Email != "ada.new@example.com" {
		t.Fatalf("confirmation: %+v", in)
	}
	if u, _ := f.svc.Find(ctx, "ada@example.com"); u.Email != "ada@example.com" || u.PendingEmail != "ada.new@example.com" {
		t.Errorf("before confirming: %+v", u)
	}
	token := tokenOf(t, in)
	if want := f.clock.Add(24 * time.Hour).UTC().Format(time.RFC3339); in.Data["expires_at"] != want {
		t.Errorf("expires_at %v, want %s", in.Data["expires_at"], want)
	}
	// The page names the new address, and opening it changes nothing.
	if rec := f.page(t, "GET", "/v1/acme/_auth/confirm-email-change?token="+token, "", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ada.new@example.com") {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if u, _ := f.svc.Find(ctx, "ada@example.com"); u.Email != "ada@example.com" {
		t.Fatal("GET changed the address")
	}
	rec := f.page(t, "POST", "/v1/acme/_auth/confirm-email-change", formType, url.Values{"token": {token}}.Encode(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "url=https://app.acme.example/account") || strings.Contains(rec.Body.String(), "bds_") {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/_auth/me", ""); code != 401 {
		t.Errorf("sessions should have ended: %d", code)
	}
	u, err := f.svc.Find(ctx, "ada.new@example.com")
	if err != nil || !u.EmailVerified || u.PendingEmail != "" || u.PreviousEmail != "ada@example.com" {
		t.Fatalf("after confirming: %+v %v", u, err)
	}
	if login("ada@example.com", "dev-p4ssw0rd!") != 401 || login("ada.new@example.com", "dev-p4ssw0rd!") != 200 {
		t.Error("the address didn't switch")
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/confirm-email-change", formType, url.Values{"token": {token}}.Encode(), ""); rec.Code != 400 {
		t.Errorf("a used link: %d", rec.Code)
	}

	// The old address is told, with a link that undoes it.
	notice := deliver(t, f, w)
	if notice.Kind != "email-changed" || notice.To[0].Email != "ada@example.com" {
		t.Fatalf("notice: %+v", notice)
	}
	undo := tokenOf(t, notice)
	if want := f.clock.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339); notice.Data["expires_at"] != want {
		t.Errorf("undo expires_at %v, want %s", notice.Data["expires_at"], want)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/revert-email-change", formType, url.Values{"token": {undo}}.Encode(), ""); rec.Code != 200 {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	if u, err = f.svc.Find(ctx, "ada@example.com"); err != nil || !u.EmailVerified || u.PreviousEmail != "" {
		t.Fatalf("after reverting: %+v %v", u, err)
	}
	if login("ada@example.com", "dev-p4ssw0rd!") != 401 { // the password can't be trusted any more
		t.Error("the old password still works after a revert")
	}
	reset := deliver(t, f, w)
	if reset.Kind != "reset-password" || reset.To[0].Email != "ada@example.com" {
		t.Fatalf("reset email: %+v", reset)
	}
	body := `{"token": "` + tokenOf(t, reset) + `", "password": "dev-p4ssw0rd!2"}`
	if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password", "application/json", body, ""); rec.Code != 204 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if login("ada@example.com", "dev-p4ssw0rd!2") != 200 {
		t.Error("the owner can't sign in after the reset")
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/revert-email-change", formType, url.Values{"token": {undo}}.Encode(), ""); rec.Code != 400 {
		t.Errorf("a used undo link: %d", rec.Code)
	}
}

func TestEmailChangeRules(t *testing.T) {
	f, w := newChangeFixture(t)
	ctx := context.Background()

	for name, tc := range map[string]struct {
		cred, body string
		want       int
	}{
		"no session":         {"", changeBody, 401},
		"wrong password":     {f.ada, `{"new_email": "x@example.com", "password": "nope-nope-nope"}`, 401},
		"the same address":   {f.ada, `{"new_email": "ADA@example.com", "password": "dev-p4ssw0rd!"}`, 400},
		"an invalid address": {f.ada, `{"new_email": "nope", "password": "dev-p4ssw0rd!"}`, 400},
		"a bad redirect":     {f.ada, `{"new_email": "x@example.com", "password": "dev-p4ssw0rd!", "redirect_to": "https://evil.example/"}`, 400},
	} {
		if code, _ := f.as(t, tc.cred, "POST", "/v1/acme/_auth/email", tc.body); code != tc.want {
			t.Errorf("%s: %d, want %d", name, code, tc.want)
		}
	}
	// An address someone else has: the same answer, and nothing is sent.
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/_auth/email", `{"new_email": "bob@example.com", "password": "dev-p4ssw0rd!"}`); code != 202 {
		t.Errorf("a taken address: %d", code)
	}
	if w.RunOnce(ctx) {
		t.Error("a message was sent for an address that is taken")
	}
	// A confirmation that arrives after the address was registered elsewhere fails.
	f.as(t, f.ada, "POST", "/v1/acme/_auth/email", `{"new_email": "late@example.com", "password": "dev-p4ssw0rd!"}`)
	token := tokenOf(t, deliver(t, f, w))
	if _, _, err := f.svc.Signup(ctx, "late@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/confirm-email-change", "application/json", `{"token": "`+token+`"}`, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_token") {
		t.Errorf("a taken address at confirmation: %d %s", rec.Code, rec.Body)
	}
	if u, _ := f.svc.Find(ctx, "ada@example.com"); u.Email != "ada@example.com" {
		t.Errorf("the address changed: %+v", u)
	}
	// A newer request supersedes an older link.
	f.as(t, f.ada, "POST", "/v1/acme/_auth/email", `{"new_email": "first@example.com", "password": "dev-p4ssw0rd!"}`)
	first := tokenOf(t, deliver(t, f, w))
	f.as(t, f.ada, "POST", "/v1/acme/_auth/email", `{"new_email": "second@example.com", "password": "dev-p4ssw0rd!"}`)
	deliver(t, f, w)
	if rec := f.page(t, "POST", "/v1/acme/_auth/confirm-email-change", "application/json", `{"token": "`+first+`"}`, ""); rec.Code != 400 {
		t.Errorf("a superseded link: %d", rec.Code)
	}
	// An expired undo link.
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/_auth/email", `{"new_email": "third@example.com", "password": "dev-p4ssw0rd!"}`); code != 202 {
		t.Fatal(code)
	}
	confirm := tokenOf(t, deliver(t, f, w))
	f.page(t, "POST", "/v1/acme/_auth/confirm-email-change", "application/json", `{"token": "`+confirm+`"}`, "")
	undo := tokenOf(t, deliver(t, f, w))
	*f.clock = f.clock.Add(8 * 24 * time.Hour)
	if rec := f.page(t, "POST", "/v1/acme/_auth/revert-email-change", "application/json", `{"token": "`+undo+`"}`, ""); rec.Code != 400 {
		t.Errorf("an expired undo link: %d", rec.Code)
	}
}

// A realm that doesn't allow it has no route, and no confirmation page.
func TestEmailChangeDisabled(t *testing.T) {
	f, _ := newVerifyFixture(t)
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/_auth/email", changeBody); code != 404 {
		t.Errorf("the route: %d", code)
	}
	if rec := f.page(t, "GET", "/v1/acme/_auth/confirm-email-change?token=x", "", "", ""); rec.Code != 404 {
		t.Errorf("the page: %d", rec.Code)
	}
}

// An administrator's change applies at once, in any realm with email: the new
// address counts as verified, both addresses are told, and the undo link works.
func TestAdminEmailChange(t *testing.T) {
	f, w := newVerifyFixture(t) // allow_email_change is off
	ctx := context.Background()
	path := "/v1/acme/_admin/users/" + f.adaID + "/email"
	if code, out := f.as(t, f.key, "POST", path, `{"email": "ada.admin@example.com"}`); code != 200 || out["email"] != "ada.admin@example.com" || out["email_verified"] != true {
		t.Fatalf("change: %d %v", code, out)
	}
	if code, _ := f.as(t, f.key, "POST", path, `{"email": "bob@example.com"}`); code != 409 {
		t.Errorf("a taken address: %d", code)
	}
	if code, _ := f.as(t, f.key, "POST", path, `{"email": "ada.admin@example.com"}`); code != 400 {
		t.Errorf("the same address: %d", code)
	}
	if code, _ := f.as(t, f.ada, "POST", path, `{"email": "x@example.com"}`); code != 401 && code != 403 {
		t.Errorf("a non-admin: %d", code)
	}
	if code, _ := f.as(t, f.key, "POST", "/v1/acme/_admin/users/nope/email", `{"email": "x@example.com"}`); code != 404 {
		t.Errorf("an unknown user: %d", code)
	}
	got := map[string]emailInput{}
	for w.RunOnce(ctx) {
		in := deliveredEmail(t, f.runner.last())
		got[in.To[0].Email] = in
	}
	old, fresh := got["ada@example.com"], got["ada.admin@example.com"]
	if old.Kind != "email-changed" || old.Data["link"] == nil || fresh.Kind != "email-changed" || fresh.Data["link"] != nil || strings.Contains(fresh.Text, "undo") {
		t.Errorf("notices: old %+v, new %+v", old, fresh)
	}
	rec := f.page(t, "POST", "/v1/acme/_auth/revert-email-change", "application/json", `{"token": "`+tokenOf(t, old)+`"}`, "")
	if rec.Code != 204 {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.svc.Find(ctx, "ada@example.com"); err != nil {
		t.Errorf("the address wasn't restored: %v", err)
	}
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditEmailChanged})
	if len(recs) != 1 || recs[0].Details["by"] != "admin" {
		t.Errorf("email_changed audit records: %+v", recs)
	}
}
