package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

const sendBody = `{"email": "Invited@Example.com", "send": true, "redirect_to": "https://app.acme.example/welcome", "locale": "es", "expires_in": "3d"}`

// An emailed invitation: the link opens a page for the invited address, the
// password creates a verified account, and no session is started.
func TestInvitationByEmail(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()

	code, out := f.as(t, f.key, "POST", "/v1/acme/_admin/invitations", sendBody)
	if code != 201 || out["sent"] != true || out["token"] != nil || out["email"] != "invited@example.com" {
		t.Fatalf("create: %d %v", code, out)
	}
	in := deliver(t, f, w)
	if in.Kind != "invitation" || in.To[0].Email != "invited@example.com" || in.Locale != "es" {
		t.Fatalf("message: %+v", in)
	}
	if want := f.clock.Add(72 * time.Hour).UTC().Format(time.RFC3339); in.Data["expires_at"] != want {
		t.Errorf("the link should live as long as the invitation: %v, want %s", in.Data["expires_at"], want)
	}
	token := tokenOf(t, in)
	if rec := f.page(t, "GET", "/v1/acme/_auth/accept-invitation?token="+token, "", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "invited@example.com") {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.svc.Find(ctx, "invited@example.com"); err == nil {
		t.Fatal("opening the link created the account")
	}
	post := func(password, confirm string) int {
		return f.page(t, "POST", "/v1/acme/_auth/accept-invitation", formType, url.Values{"token": {token}, "password": {password}, "password_confirm": {confirm}}.Encode(), "").Code
	}
	if post("short", "short") != 400 || post("dev-p4ssw0rd!", "other") != 400 {
		t.Error("a refused password must show the form again")
	}
	rec := f.page(t, "POST", "/v1/acme/_auth/accept-invitation", formType, url.Values{"token": {token}, "password": {"dev-p4ssw0rd!"}, "password_confirm": {"dev-p4ssw0rd!"}}.Encode(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "url=https://app.acme.example/welcome") || strings.Contains(rec.Body.String(), "bds_") {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	u, err := f.svc.Find(ctx, "invited@example.com")
	if err != nil || !u.EmailVerified {
		t.Fatalf("the account: %+v %v", u, err)
	}
	if got := f.page(t, "POST", "/v1/acme/_auth/login", "application/json", `{"email": "invited@example.com", "password": "dev-p4ssw0rd!"}`, "").Code; got != 200 {
		t.Errorf("login: %d", got)
	}
	if post("dev-p4ssw0rd!", "dev-p4ssw0rd!") != 400 {
		t.Error("a used link")
	}
	// The invitation is used up.
	if _, out := f.as(t, f.key, "GET", "/v1/acme/_admin/invitations", ""); len(out["items"].([]any)) != 0 {
		t.Errorf("invitations left: %v", out)
	}
}

func TestInvitationByEmailJSONRevokeAndRules(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	admin := "/v1/acme/_admin/invitations"

	_, out := f.as(t, f.key, "POST", admin, `{"email": "json@example.com", "send": true}`)
	token := tokenOf(t, deliver(t, f, w))
	body := `{"token": "` + token + `", "password": "dev-p4ssw0rd!", "locale": "es"}`
	if rec := f.page(t, "POST", "/v1/acme/_auth/accept-invitation", "application/json", `{"token": "`+token+`", "password": "short"}`, ""); rec.Code != 400 {
		t.Errorf("policy: %d", rec.Code)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/accept-invitation", "application/json", body, ""); rec.Code != 204 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if u, err := f.svc.Find(ctx, "json@example.com"); err != nil || u.Locale != "es" {
		t.Errorf("the account: %+v %v", u, err)
	}
	_ = out

	// A revoked invitation's link no longer works.
	_, out = f.as(t, f.key, "POST", admin, `{"email": "revoked@example.com", "send": true}`)
	revoked := tokenOf(t, deliver(t, f, w))
	if code, _ := f.as(t, f.key, "DELETE", admin+"/"+out["id"].(string), ""); code != 204 {
		t.Fatal(code)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/accept-invitation", "application/json", `{"token": "`+revoked+`", "password": "dev-p4ssw0rd!"}`, ""); rec.Code != 400 {
		t.Errorf("a revoked invitation: %d", rec.Code)
	}
	if _, err := f.svc.Find(ctx, "revoked@example.com"); err == nil {
		t.Error("a revoked invitation made an account")
	}

	for name, body := range map[string]string{
		"send without an address": `{"send": true}`,
		"a bad redirect":          `{"email": "x@example.com", "send": true, "redirect_to": "https://evil.example/"}`,
		"redirect without send":   `{"email": "x@example.com", "redirect_to": "https://app.acme.example/welcome"}`,
		"locale without send":     `{"email": "x@example.com", "locale": "es"}`,
	} {
		if code, _ := f.as(t, f.key, "POST", admin, body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	// Without send, an invitation works as before: a token, no email.
	if code, out := f.as(t, f.key, "POST", admin, `{"email": "plain@example.com"}`); code != 201 || out["token"] == nil || out["sent"] != nil {
		t.Errorf("a plain invitation: %d %v", code, out)
	}
	if w.RunOnce(ctx) {
		t.Error("a plain invitation sent an email")
	}
	// An address that registered meanwhile can't be taken over by the link.
	f.as(t, f.key, "POST", admin, `{"email": "raced@example.com", "send": true}`)
	raced := tokenOf(t, deliver(t, f, w))
	if _, _, err := f.svc.Signup(ctx, "raced@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/accept-invitation", "application/json", `{"token": "`+raced+`", "password": "dev-p4ssw0rd!2"}`, ""); rec.Code != 400 {
		t.Errorf("an address registered meanwhile: %d", rec.Code)
	}
}

// The email limits apply: past them the invitation isn't left behind.
func TestInvitationByEmailLimit(t *testing.T) {
	f, _ := newVerifyFixture(t)
	body := `{"email": "many@example.com", "send": true}`
	for i := 0; i < 3; i++ {
		if code, _ := f.as(t, f.key, "POST", "/v1/acme/_admin/invitations", body); code != 201 {
			t.Fatalf("invitation %d: %d", i, code)
		}
	}
	if code, _ := f.as(t, f.key, "POST", "/v1/acme/_admin/invitations", body); code != 429 {
		t.Errorf("past the limit: %d", code)
	}
	if _, out := f.as(t, f.key, "GET", "/v1/acme/_admin/invitations", ""); len(out["items"].([]any)) != 3 {
		t.Errorf("the refused invitation was kept: %v", out)
	}
}
