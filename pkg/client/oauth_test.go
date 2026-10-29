package client

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestPKCEChallengeMatchesRFC7636(t *testing.T) {
	if got := pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge = %q", got)
	}
	v, ch := newPKCE()
	if len(v) != 43 || pkceChallenge(v) != ch {
		t.Errorf("verifier %q challenge %q", v, ch)
	}
}

func TestParseOAuthReturn(t *testing.T) {
	r := ParseOAuthReturn("myapp://cb?x=1&backd_code=abc&backd_org=acme&backd_provider=google")
	if r == nil || r.Code != "abc" || r.Org != "acme" || r.Provider != "google" {
		t.Errorf("%+v", r)
	}
	if ParseOAuthReturn("https://app.example/?q=1") != nil || ParseOAuthReturn("::") != nil {
		t.Error("an address without a sign-in is nil")
	}
	if r := ParseOAuthReturn("https://app.example/?backd_linked=google"); r == nil || r.Linked != "google" {
		t.Errorf("%+v", r)
	}
}

func TestRedirectSignInRoundTrip(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/oauth/google/start", "", `{"authorize_url":"https://accounts.example/auth?x=1"}`)
	rt.json("POST /_auth/token", "", sessionJSON)
	c := authClient(t, rt)

	u, err := c.Auth().SignInWith(bg, "google", OAuthOptions{RedirectTo: "http://localhost:9999/cb", Locale: "es"})
	if err != nil || u != "https://accounts.example/auth?x=1" {
		t.Fatalf("%v %q", err, u)
	}
	start := rt.sent["POST /_auth/oauth/google/start"]
	challenge, _ := start["code_challenge"].(string)
	if start["intent"] != "signin" || start["redirect_to"] != "http://localhost:9999/cb" || challenge == "" || start["invitation"] != nil {
		t.Errorf("start sent %v", start)
	}
	if rt.auth["POST /_auth/oauth/google/start"] != "" {
		t.Error("a sign-in start sends no credentials")
	}

	step, err := c.Auth().CompleteSignIn(bg, "http://localhost:9999/cb?backd_code=one-time")
	if err != nil || step.Kind != StepSession || step.Session.Token != "bds_1" {
		t.Fatalf("%v %+v", err, step)
	}
	tok := rt.sent["POST /_auth/token"]
	if tok["code"] != "one-time" || pkceChallenge(tok["code_verifier"].(string)) != challenge {
		t.Errorf("token sent %v", tok)
	}
	// the pending sign-in is used once
	if _, err := c.Auth().CompleteSignIn(bg, "http://localhost:9999/cb?backd_code=again"); !errors.Is(err, ErrValidation) {
		t.Errorf("a second completion: %v", err)
	}
}

func TestCompleteSignInOtherOutcomes(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/oauth/google/start", "", `{"authorize_url":"u"}`)
	c := authClient(t, rt)

	if step, err := c.Auth().CompleteSignIn(bg, "http://x/?plain=1"); step != nil || err != nil {
		t.Errorf("no sign-in in the address: %v %v", step, err)
	}
	if _, err := c.Auth().CompleteSignIn(bg, "http://x/?backd_code=c"); err == nil || err.(*Error).Code != "wrong_device" {
		t.Errorf("no pending sign-in: %v", err)
	}
	c.Auth().SignInWith(bg, "google", OAuthOptions{})
	_, err := c.Auth().CompleteSignIn(bg, "http://x/?backd_error=account_exists")
	var e *Error
	if !errors.As(err, &e) || e.Status != 409 || e.Code != "account_exists" || !errors.Is(err, ErrConflict) || e.Details[0].Reason != "google" {
		t.Errorf("%#v", err)
	}
	_, err = c.Auth().CompleteSignIn(bg, "http://x/?backd_error=surprise&backd_provider=apple")
	if !errors.As(err, &e) || e.Code != "provider_error" || e.Status != 502 || e.Details[0].Reason != "apple" {
		t.Errorf("unknown error codes become provider_error: %#v", err)
	}
	step, err := c.Auth().CompleteSignIn(bg, "http://x/?backd_linked=google")
	if err != nil || step.Kind != StepLinked || step.Linked != "google" {
		t.Errorf("%v %+v", err, step)
	}
}

func TestPendingSignInExpires(t *testing.T) {
	s := &MemoryOAuthStorage{}
	savePending(bg, s, pending{Verifier: "v", Provider: "google", Intent: "signin"})
	if loadPending(bg, s) == nil {
		t.Error("a fresh pending sign-in")
	}
	old := time.Now().Add(-11 * time.Minute).UnixMilli()
	s.Set(bg, `{"verifier":"v","at":`+strconv.FormatInt(old, 10)+`}`)
	if loadPending(bg, s) != nil {
		t.Error("a sign-in older than the server keeps its side")
	}
	s.Set(bg, `{not json`)
	if loadPending(bg, s) != nil {
		t.Error("unreadable is none")
	}
}

func TestLinkingUsesTheSessionAndAReauthProof(t *testing.T) {
	rt := newRoutes()
	rt.h["POST /_auth/oauth/google/start"] = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		w.Write([]byte(`{"authorize_url":"u"}`))
	}
	rt.json("DELETE /_auth/identities/google", "204", ``)
	rt.json("POST /_auth/oauth/apple/id-token", "", `{"linked":"apple"}`)
	c := authClient(t, rt)
	c.store.Set(bg, "tok")
	if _, err := c.Auth().LinkProvider(bg, "google", OAuthOptions{}); err != nil {
		t.Fatal(err)
	}
	if rt.auth["POST /_auth/oauth/google/start"] != "Bearer tok" || rt.sent["POST /_auth/oauth/google/start"]["intent"] != "link" {
		t.Errorf("link start: %q %v", rt.auth["POST /_auth/oauth/google/start"], rt.sent["POST /_auth/oauth/google/start"])
	}
	c.proof.Store(&reauthProof{token: "rt", expires: time.Now().Add(time.Minute)})
	if err := c.Auth().UnlinkProvider(bg, "google", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth().LinkProviderWithIDToken(bg, "apple", IDTokenInput{IDToken: "jwt", Nonce: "n"}); err != nil {
		t.Fatal(err)
	}
	if got := rt.sent["POST /_auth/oauth/apple/id-token"]; got["intent"] != "link" || got["id_token"] != "jwt" || got["nonce"] != "n" || got["locale"] != nil {
		t.Errorf("id-token sent %v", got)
	}
}

func TestSignInWithIDToken(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/oauth/apple/id-token", "", `{"mfa_required":true,"challenge":"c","methods":["totp"]}`)
	c := authClient(t, rt)
	step, err := c.Auth().SignInWithIDToken(bg, "apple", IDTokenInput{IDToken: "jwt", AuthorizationCode: "ac"})
	if err != nil || step.Kind != StepMFA {
		t.Fatalf("%v %+v", err, step)
	}
	if got := rt.sent["POST /_auth/oauth/apple/id-token"]; got["authorization_code"] != "ac" || got["intent"] != nil {
		t.Errorf("sent %v", got)
	}
}

func TestMagicLink(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/magic-link", "", `{"request_id":"rq1"}`)
	rt.json("POST /_auth/magic-link/verify", "", sessionJSON)
	c := authClient(t, rt)
	if _, err := c.Auth().MagicLink.VerifyCode(bg, "123"); err == nil {
		t.Error("a code without a request")
	}
	id, err := c.Auth().MagicLink.Request(bg, "ada@example.com", MagicLinkOptions{RedirectTo: "http://x/cb"})
	if err != nil || id != "rq1" {
		t.Fatalf("%v %q", err, id)
	}
	challenge := rt.sent["POST /_auth/magic-link"]["code_challenge"].(string)
	step, err := c.Auth().MagicLink.VerifyCode(bg, " 123456 ")
	if err != nil || step.Session == nil {
		t.Fatalf("%v %+v", err, step)
	}
	v := rt.sent["POST /_auth/magic-link/verify"]
	if v["request_id"] != "rq1" || v["code"] != "123456" || pkceChallenge(v["code_verifier"].(string)) != challenge {
		t.Errorf("verify sent %v", v)
	}
}

func TestMFA(t *testing.T) {
	rt := newRoutes()
	rt.json("GET /_auth/mfa", "", `{"available":true,"enabled":false,"methods":[],"recovery_codes_left":0,"required":true}`)
	rt.json("POST /_auth/mfa/totp/setup", "", `{"setup_id":"su1","secret":"S","otpauth_uri":"otpauth://x","qr":"data:","expires_at":"x"}`)
	rt.json("POST /_auth/mfa/totp/confirm", "", `{"recovery_codes":["r1","r2"]}`)
	rt.json("DELETE /_auth/mfa/totp", "204", ``)
	rt.json("POST /_auth/mfa/recovery-codes", "", `{"recovery_codes":["n1"]}`)
	c := authClient(t, rt)
	c.store.Set(bg, "tok")
	m := c.Auth().MFA
	st, err := m.Status(bg)
	if err != nil || !st.Required || st.Enabled {
		t.Fatalf("%v %+v", err, st)
	}
	if su, err := m.Setup(bg, ""); err != nil || su.SetupID != "su1" || rt.auth["POST /_auth/mfa/totp/setup"] != "Bearer tok" {
		t.Errorf("signed-in setup: %v %+v", err, su)
	}
	if _, err := m.Setup(bg, "enroll1"); err != nil || rt.auth["POST /_auth/mfa/totp/setup"] != "" || rt.sent["POST /_auth/mfa/totp/setup"]["challenge"] != "enroll1" {
		t.Errorf("enrollment setup: %v %q %v", err, rt.auth["POST /_auth/mfa/totp/setup"], rt.sent["POST /_auth/mfa/totp/setup"])
	}
	res, err := m.Confirm(bg, MFAConfirmInput{SetupID: "su1", Code: "123456"})
	if err != nil || len(res.RecoveryCodes) != 2 || res.Session != nil || res.Consent != nil {
		t.Errorf("%v %+v", err, res)
	}
	if err := m.Disable(bg); err != nil {
		t.Error(err)
	}
	if codes, err := m.RegenerateRecoveryCodes(bg); err != nil || codes[0] != "n1" {
		t.Errorf("%v %v", err, codes)
	}
	// confirming an enrollment finishes the sign-in
	rt.json("POST /_auth/mfa/totp/confirm", "", `{"recovery_codes":["r1"],`+sessionJSON[1:])
	res, err = m.Confirm(bg, MFAConfirmInput{SetupID: "su1", Code: "1", Challenge: "enroll1"})
	if err != nil || res.Session == nil || res.Session.Token != "bds_1" {
		t.Errorf("%v %+v", err, res)
	}
	rt.json("POST /_auth/mfa/totp/confirm", "", `{"recovery_codes":["r1"],"consent_required":true,"challenge":"c","consents":[{"name":"t","version":"1"}]}`)
	res, err = m.Confirm(bg, MFAConfirmInput{SetupID: "su1", Code: "1", Challenge: "enroll1"})
	if err != nil || res.Consent == nil || res.Consent.Kind != StepConsent || res.Session != nil {
		t.Errorf("%v %+v", err, res)
	}
}

func TestSignUpWithOrgIsKeptByTheOrganizationsClient(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/orgs", "", `{"organization":{"id":"o1","name":"Acme"},"token":"bds_org","session_id":"s","expires_at":"x","org":"o1"}`)
	c := authClient(t, rt)
	res, err := c.Auth().SignUpWithOrg(bg, SignUpWithOrgInput{Email: "a@b.c", Password: "p", Name: "Acme", Slug: "acme"})
	if err != nil || res.Token != "bds_org" || string(res.Organization) == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if tok, _ := c.store.Get(bg); tok != "" {
		t.Errorf("the realm's own client holds %q", tok)
	}
	oc, _ := c.Org("acme")
	if tok, _ := oc.store.Get(bg); tok != "bds_org" {
		t.Errorf("the organization's client holds %q", tok)
	}
}
