package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// idToken posts a native sign-in: the ID token the provider (the fake) signed for the claims, and the nonce.
func (f *oauthFixture) idToken(t *testing.T, provider string, claims map[string]any, body map[string]any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	b := map[string]any{"id_token": f.idp.IDToken(t, claims), "nonce": "raw-nonce"}
	for k, v := range body {
		b[k] = v
	}
	data, _ := json.Marshal(b)
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Content-Type"] = "application/json"
	rec, out := f.doH(t, "POST", oauthBase+"/"+provider+"/id-token", string(data), hdr)
	return rec.Code, out
}

func nativeGoogle(sub, email string, verified bool, nonce string) map[string]any {
	c := googleUser(sub, email, verified)
	c["aud"] = "g-native"
	c["nonce"] = nonce
	return c
}

func TestNativeSignInGivesASession(t *testing.T) {
	f := newOAuthFixture(t)
	code, out := f.idToken(t, "google", nativeGoogle("g-n1", "native@example.com", true, "raw-nonce"), nil, nil)
	if code != 200 || out["token"] == nil || out["user"].(map[string]any)["email"] != "native@example.com" {
		t.Fatalf("sign-in: %d %v", code, out)
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(out["token"].(string)))
	if ids := me["identities"].([]any); len(ids) != 1 || ids[0].(map[string]any)["provider"] != "google" {
		t.Errorf("me: %v", me)
	}
	// Again: the same user. The SDKs may send the nonce hashed.
	sum := sha256.Sum256([]byte("raw-nonce"))
	code, out2 := f.idToken(t, "google", nativeGoogle("g-n1", "native@example.com", true, hex.EncodeToString(sum[:])), nil, nil)
	if code != 200 || out2["user"].(map[string]any)["id"] != out["user"].(map[string]any)["id"] {
		t.Errorf("second sign-in: %d %v", code, out2)
	}
	// The web client's audience is good here too; a cookie session works like at login.
	web := googleUser("g-n1", "native@example.com", true)
	web["nonce"] = "raw-nonce"
	if code, _ := f.idToken(t, "google", web, nil, nil); code != 200 {
		t.Errorf("web audience: %d", code)
	}
}

func TestNativeSignInRefusesWhatDoesNotVerify(t *testing.T) {
	f := newOAuthFixture(t)
	good := func() map[string]any { return nativeGoogle("g-n1", "native@example.com", true, "raw-nonce") }
	with := func(k string, v any) map[string]any { c := good(); c[k] = v; return c }
	for name, claims := range map[string]map[string]any{
		"another nonce":    with("nonce", "other"),
		"no nonce":         with("nonce", ""),
		"another audience": with("aud", "someone-else"),
		"another issuer":   with("iss", "https://evil.example"),
		"expired":          with("exp", time.Now().Add(-time.Hour).Unix()),
	} {
		if code, out := f.idToken(t, "google", claims, nil, nil); code != 401 || errCode(out) != "invalid_token" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// Garbage, a missing nonce in the body, an unknown provider, a wrong type.
	if code, out := f.idToken(t, "google", good(), map[string]any{"id_token": "not.a.jwt"}, nil); code != 401 {
		t.Errorf("garbage: %d %v", code, out)
	}
	if code, _ := f.idToken(t, "google", good(), map[string]any{"nonce": ""}, nil); code != 400 {
		t.Errorf("no nonce in the body: %d", code)
	}
	if code, _ := f.idToken(t, "github", good(), nil, nil); code != 404 {
		t.Errorf("unknown provider: %d", code)
	}
	if code, _ := f.idToken(t, "google", good(), map[string]any{"intent": "hijack"}, nil); code != 400 {
		t.Errorf("unknown intent: %d", code)
	}
	if users, _ := f.svc.List(context.Background()); len(users) != 3 {
		t.Errorf("a refused token made a user: %d users", len(users))
	}
}

func TestNativeSignInFollowsTheRealmsRules(t *testing.T) {
	f := newOAuthFixture(t)
	// ada@example.com is verified: a verified Google address links to her.
	code, out := f.idToken(t, "google", nativeGoogle("g-ada", "ada@example.com", true, "raw-nonce"), nil, nil)
	if code != 200 || out["user"].(map[string]any)["id"] != f.adaID {
		t.Errorf("auto-link: %d %v", code, out)
	}
	// An address Google doesn't vouch for, and an unverified backd account, don't link.
	if code, out := f.idToken(t, "google", nativeGoogle("g-bob", "bob@example.com", false, "raw-nonce"), nil, nil); code != 409 || errCode(out) != "account_exists" {
		t.Errorf("unverified at the provider: %d %v", code, out)
	}
	if code, out := f.idToken(t, "google", nativeGoogle("g-carl", "carl@example.com", true, "raw-nonce"), nil, nil); code != 409 || errCode(out) != "account_exists" {
		t.Errorf("unverified backd account: %d %v", code, out)
	}
	f.svc.Settings.Signup = registry.SignupClosed
	if code, out := f.idToken(t, "google", nativeGoogle("g-x", "x@example.com", true, "raw-nonce"), nil, nil); code != 403 || errCode(out) != "signup_closed" {
		t.Errorf("closed: %d %v", code, out)
	}
	f.svc.Settings.Signup = registry.SignupInvite
	if code, out := f.idToken(t, "google", nativeGoogle("g-x", "x@example.com", true, "raw-nonce"), nil, nil); code != 400 || errCode(out) != "invitation_invalid" {
		t.Errorf("invite without an invitation: %d %v", code, out)
	}
	_, token, _ := f.svc.CreateInvitation(context.Background(), "x@example.com", time.Hour, "admin")
	if code, out := f.idToken(t, "google", nativeGoogle("g-x", "x@example.com", true, "raw-nonce"), map[string]any{"invitation": token}, nil); code != 200 {
		t.Errorf("with an invitation: %d %v", code, out)
	}
	f.svc.Settings.Signup = registry.SignupOpen
	f.svc.Settings.Account.RequireVerifiedEmail = true
	ms := map[string]any{"iss": "https://login.microsoftonline.com/t1/v2.0", "aud": "00000000-0000-0000-0000-000000000001", "tid": "t1", "oid": "o9", "sub": "s", "email": "m@corp.example", "nonce": "raw-nonce"}
	if code, out := f.idToken(t, "microsoft", ms, nil, nil); code != 403 || errCode(out) != "email_not_verified" {
		t.Errorf("unverified: %d %v", code, out)
	}
	if code, out := f.idToken(t, "google", map[string]any{"iss": "https://accounts.google.com", "aud": "g-native", "sub": "g-none", "nonce": "raw-nonce"}, nil, nil); code != 400 || errCode(out) != "email_required" {
		t.Errorf("no address: %d %v", code, out)
	}
	f.svc.Settings.Account.RequireVerifiedEmail = false
	if err := f.svc.SetDisabled(context.Background(), "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if code, out := f.idToken(t, "google", nativeGoogle("g-ada", "ada@example.com", true, "raw-nonce"), nil, nil); code != 403 || errCode(out) != "signin_refused" {
		t.Errorf("disabled: %d %v", code, out)
	}
}

func TestNativeApple(t *testing.T) {
	f := newOAuthFixture(t)
	apple := map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.ios", "sub": "a-n1", "email": "x@privaterelay.appleid.com", "email_verified": "true", "nonce": "raw-nonce"}
	f.idp.GrantCode("abc", map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.ios", "sub": "a-n1"})
	code, out := f.idToken(t, "apple", apple, map[string]any{"authorization_code": "abc"}, nil)
	if code != 200 || out["user"].(map[string]any)["email_verified"] != true {
		t.Errorf("apple: %d %v", code, out)
	}
	apple["aud"] = "com.someone.else"
	if code, _ := f.idToken(t, "apple", apple, map[string]any{"authorization_code": "abc"}, nil); code != 401 {
		t.Errorf("another bundle id: %d", code)
	}
}

func TestNativeLinking(t *testing.T) {
	f := newOAuthFixture(t)
	claims := nativeGoogle("g-ada", "ada.work@gmail.example", true, "raw-nonce")
	if code, _ := f.idToken(t, "google", claims, map[string]any{"intent": "link"}, nil); code != 401 {
		t.Errorf("linking without a session: %d", code)
	}
	code, out := f.idToken(t, "google", claims, map[string]any{"intent": "link"}, bearer(f.ada))
	if code != 200 || out["linked"] != "google" {
		t.Fatalf("link: %d %v", code, out)
	}
	if code, out := f.idToken(t, "google", claims, map[string]any{"intent": "link"}, bearer(f.bob)); code != 409 || errCode(out) != "link_conflict" {
		t.Errorf("an account of another user: %d %v", code, out)
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(f.ada))
	if ids := me["identities"].([]any); len(ids) != 2 {
		t.Errorf("me: %v", me)
	}
}

func TestNativeSignInIsRateLimited(t *testing.T) {
	f := newOAuthFixture(t)
	for i := range 30 {
		if code, _ := f.idToken(t, "google", nativeGoogle("g-n1", "native@example.com", true, "raw-nonce"), nil, nil); code != 200 {
			t.Fatalf("sign-in %d: %d", i, code)
		}
	}
	if code, _ := f.idToken(t, "google", nativeGoogle("g-n1", "native@example.com", true, "raw-nonce"), nil, nil); code != 429 {
		t.Errorf("the 31st: %d", code)
	}
}
