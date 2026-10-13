package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/fernandezvara/backd/internal/auth"
)

// mfaFixture is a user with a password and a TOTP secret, enrolled the way 13.2's
// endpoints do it.
type mfaHTTP struct {
	*authFixture
	user   auth.User
	secret string
}

func newMFAHTTP(t *testing.T) *mfaHTTP {
	t.Helper()
	f := newAuthFixture(t)
	f.svc.Realm = "acme"
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	pw := "dev-p4ssw0rd!"
	u, err := f.svc.Create(context.Background(), "mia@example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "acme", AccountName: u.Email})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := f.svc.SealToken(key.Secret())
	hash := f.svc.Cipher.MAC("recovery", "acme", u.ID, "ABCDEABCDE")
	if err := f.store.PutMFA(context.Background(), auth.MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: time.Now(), RecoveryCodes: []string{hash}}); err != nil {
		t.Fatal(err)
	}
	return &mfaHTTP{authFixture: f, user: u, secret: key.Secret()}
}

func (f *mfaHTTP) code(t *testing.T, offset time.Duration) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(f.secret, time.Now().Add(offset), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *mfaHTTP) challenge(t *testing.T) string {
	t.Helper()
	rec, out := f.do(t, "POST", authBase+"/login", `{"email": "mia@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != 200 || out["mfa_required"] != true || out["challenge"] == nil {
		t.Fatalf("login: %d %v", rec.Code, out)
	}
	return out["challenge"].(string)
}

func TestLoginWithMFAAnswersAChallenge(t *testing.T) {
	f := newMFAHTTP(t)
	rec, out := f.do(t, "POST", authBase+"/login", `{"email": "mia@example.com", "password": "dev-p4ssw0rd!"}`)
	methods, _ := out["methods"].([]any)
	if rec.Code != 200 || out["mfa_required"] != true || len(methods) != 2 || methods[0] != "totp" || methods[1] != "recovery_code" {
		t.Fatalf("login: %d %v", rec.Code, out)
	}
	for _, k := range []string{"token", "session_id", "user"} {
		if _, ok := out[k]; ok {
			t.Errorf("a challenge carries %q: no half sessions", k)
		}
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("a cookie was set before the second factor")
	}
	// The challenge is not a session: nothing accepts it as one.
	token := out["challenge"].(string)
	if code, _ := f.as(t, token, "GET", authBase+"/me"); code != 401 {
		t.Errorf("challenge as a session: %d", code)
	}
}

// as is a request with a bearer token, for the auth fixture.
func (f *authFixture) as(t *testing.T, token, method, path string) (int, map[string]any) {
	t.Helper()
	rec, out := f.doH(t, method, path, "", bearer(token))
	return rec.Code, out
}

func TestMFAVerifyIssuesTheSession(t *testing.T) {
	f := newMFAHTTP(t)
	ch := f.challenge(t)
	body := `{"challenge": "` + ch + `", "method": "totp", "code": "` + f.code(t, 0) + `"}`
	rec, out := f.do(t, "POST", authBase+"/mfa/verify", body)
	token, _ := out["token"].(string)
	if rec.Code != 200 || !strings.HasPrefix(token, "bds_") || out["user"] == nil || out["recovery_codes_left"] != nil {
		t.Fatalf("verify: %d %v", rec.Code, out)
	}
	if code, me := f.as(t, token, "GET", authBase+"/me"); code != 200 || me["email"] != "mia@example.com" {
		t.Errorf("me: %d %v", code, me)
	}
	// The challenge worked once.
	rec, out = f.do(t, "POST", authBase+"/mfa/verify", body)
	if rec.Code != 400 || errCode(out) != "invalid_challenge" {
		t.Errorf("reuse: %d %v", rec.Code, out)
	}
}

func TestMFAVerifyErrors(t *testing.T) {
	f := newMFAHTTP(t)
	ch := f.challenge(t)
	rec, out := f.do(t, "POST", authBase+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "000000"}`)
	details, _ := out["error"].(map[string]any)["details"].([]any)
	if rec.Code != 400 || errCode(out) != "invalid_code" || len(details) != 1 || details[0].(map[string]any)["reason"] != "4" {
		t.Errorf("wrong code: %d %v", rec.Code, out)
	}
	for _, body := range []string{
		`{"challenge": "nope", "method": "totp", "code": "000000"}`,
		`{"challenge": "", "method": "totp", "code": "000000"}`,
	} {
		rec, out := f.do(t, "POST", authBase+"/mfa/verify", body)
		if rec.Code != 400 || errCode(out) != "invalid_challenge" {
			t.Errorf("%s: %d %v", body, rec.Code, out)
		}
	}
	for _, body := range []string{`{}`, `{"challenge": "x", "method": "totp"}`, `{"challenge": "x", "method": "totp", "code": 1}`, `{"challenge": "x", "method": "totp", "code": "1", "extra": 1}`} {
		if rec, out := f.do(t, "POST", authBase+"/mfa/verify", body); rec.Code != 400 || errCode(out) != "validation_error" {
			t.Errorf("%s: %d %v", body, rec.Code, out)
		}
	}
}

func TestMFAVerifyWithARecoveryCode(t *testing.T) {
	f := newMFAHTTP(t)
	ch := f.challenge(t)
	rec, out := f.do(t, "POST", authBase+"/mfa/verify", `{"challenge": "`+ch+`", "method": "recovery_code", "code": "abcde-abcde"}`)
	if rec.Code != 200 || out["recovery_codes_left"] != float64(0) || out["token"] == nil {
		t.Errorf("recovery: %d %v", rec.Code, out)
	}
}

func TestMFAVerifyLocksAfterTenWrongCodes(t *testing.T) {
	f := newMFAHTTP(t)
	for i := 0; i < 10; i++ {
		ch := f.challenge(t)
		if rec, _ := f.do(t, "POST", authBase+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "000000"}`); rec.Code != 400 {
			t.Fatalf("wrong code %d: %d", i, rec.Code)
		}
	}
	ch := f.challenge(t)
	rec, out := f.do(t, "POST", authBase+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "`+f.code(t, 0)+`"}`)
	if rec.Code != http.StatusTooManyRequests || errCode(out) != "mfa_locked" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("locked: %d %v", rec.Code, out)
	}
}

func TestMFARequiredRoleAsksToEnroll(t *testing.T) {
	f := newAuthFixture(t)
	f.svc.Settings.MFA.RequiredAll = true
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != 200 || out["mfa_enrollment_required"] != true || out["challenge"] == nil || out["token"] != nil {
		t.Errorf("signup: %d %v", rec.Code, out)
	}
	rec, out = f.do(t, "POST", authBase+"/login", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != 200 || out["mfa_enrollment_required"] != true || out["mfa_required"] != nil {
		t.Errorf("login: %d %v", rec.Code, out)
	}
}

func TestRulesAndFunctionsSeeHowTheSessionWasMade(t *testing.T) {
	u := auth.User{ID: "u1", Email: "mia@example.com", Roles: []string{"owner"}}
	viaTOTP := auth.Caller{User: &auth.Principal{User: u, Session: auth.Session{Methods: []string{"password", "totp"}, MFA: true}}}
	ru := rulesUser(viaTOTP)
	if !ru.MFA || len(ru.Methods) != 2 || ru.Methods[1] != "totp" {
		t.Errorf("rules user: %+v", ru)
	}
	var env map[string]any
	if err := json.Unmarshal(userEnvelope(viaTOTP), &env); err != nil {
		t.Fatal(err)
	}
	if env["mfa"] != true || len(env["methods"].([]any)) != 2 {
		t.Errorf("ctx.user: %v", env)
	}
	// An API key acting for the user is never MFA, whatever the user has.
	viaKey := auth.Caller{Key: &auth.APIKey{Name: "k"}, User: viaTOTP.User}
	if ru := rulesUser(viaKey); ru.MFA || len(ru.Methods) != 1 || ru.Methods[0] != "key" {
		t.Errorf("key rules user: %+v", ru)
	}
	// Sessions from before the methods were recorded have none, and the lists are never null.
	old := auth.Caller{User: &auth.Principal{User: u}}
	if err := json.Unmarshal(userEnvelope(old), &env); err != nil || env["mfa"] != false || env["methods"] == nil {
		t.Errorf("old session: %v %v", env, err)
	}
}

// enrollFixture is a signed-in user with no second factor.
func newEnrollHTTP(t *testing.T) (*authFixture, string) {
	t.Helper()
	f := newAuthFixture(t)
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	token, _ := f.signup(t, "ivy@example.com")
	return f, token
}

// send is a request with a bearer token and a JSON body.
func (f *authFixture) send(t *testing.T, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	hdr := bearer(token)
	hdr["Content-Type"] = "application/json"
	rec, out := f.doH(t, method, path, body, hdr)
	return rec.Code, out
}

func TestEnrollingTOTPOverHTTP(t *testing.T) {
	f, token := newEnrollHTTP(t)
	code, st := f.as(t, token, "GET", authBase+"/mfa")
	if code != 200 || st["enabled"] != false || st["available"] != true || st["required"] != false || st["recovery_codes_left"] != float64(0) {
		t.Fatalf("status: %d %v", code, st)
	}
	if rec, _ := f.do(t, "GET", authBase+"/mfa", ""); rec.Code != 401 {
		t.Error("status without a session")
	}
	code, setup := f.send(t, token, "POST", authBase+"/mfa/totp/setup", "")
	secret, _ := setup["secret"].(string)
	if code != 200 || secret == "" || !strings.HasPrefix(setup["otpauth_uri"].(string), "otpauth://totp/acme:ivy@example.com") ||
		!strings.HasPrefix(setup["qr"].(string), "data:image/png;base64,") || setup["setup_id"] == "" {
		t.Fatalf("setup: %d %v", code, setup)
	}
	body := func(c string) string {
		return `{"setup_id": "` + setup["setup_id"].(string) + `", "code": "` + c + `"}`
	}
	if code, out := f.send(t, token, "POST", authBase+"/mfa/totp/confirm", body("000000")); code != 400 || errCode(out) != "invalid_code" {
		t.Errorf("wrong code: %d %v", code, out)
	}
	good, _ := totp.GenerateCode(secret, f.svc.Clock())
	code, out := f.send(t, token, "POST", authBase+"/mfa/totp/confirm", body(good))
	codes, _ := out["recovery_codes"].([]any)
	if code != 200 || len(codes) != 10 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if code, st = f.as(t, token, "GET", authBase+"/mfa"); code != 200 || st["enabled"] != true || st["recovery_codes_left"] != float64(10) {
		t.Errorf("status after: %d %v", code, st)
	}
	// A second setup is a conflict; regenerating gives new codes; disabling turns it off.
	if code, out := f.send(t, token, "POST", authBase+"/mfa/totp/setup", ""); code != 409 || errCode(out) != "mfa_already_enabled" {
		t.Errorf("setup again: %d %v", code, out)
	}
	code, out = f.send(t, token, "POST", authBase+"/mfa/recovery-codes", "")
	if again, _ := out["recovery_codes"].([]any); code != 200 || len(again) != 10 || again[0] == codes[0] {
		t.Errorf("regenerate: %d %v", code, out)
	}
	if code, _ := f.send(t, token, "DELETE", authBase+"/mfa/totp", ""); code != 204 {
		t.Errorf("disable: %d", code)
	}
	if code, out := f.send(t, token, "DELETE", authBase+"/mfa/totp", ""); code != 409 || errCode(out) != "mfa_not_enabled" {
		t.Errorf("disable twice: %d %v", code, out)
	}
	if code, out := f.send(t, token, "POST", authBase+"/mfa/recovery-codes", ""); code != 409 || errCode(out) != "mfa_not_enabled" {
		t.Errorf("regenerate when off: %d %v", code, out)
	}
}

func TestMFARefusedWhenTheRealmTurnsItOff(t *testing.T) {
	f, token := newEnrollHTTP(t)
	f.svc.Settings.MFA.Off = true
	code, st := f.as(t, token, "GET", authBase+"/mfa")
	if code != 200 || st["available"] != false {
		t.Errorf("status: %d %v", code, st)
	}
	if code, out := f.send(t, token, "POST", authBase+"/mfa/totp/setup", ""); code != 403 || errCode(out) != "mfa_disabled" {
		t.Errorf("setup: %d %v", code, out)
	}
}

func TestRequiredRoleKeepsMFAOn(t *testing.T) {
	f := newMFAHTTP(t)
	f.svc.Settings.MFA.RequiredAll = true
	token := f.sessionOf(t)
	if code, out := f.send(t, token, "DELETE", authBase+"/mfa/totp", ""); code != 403 || errCode(out) != "mfa_required_by_role" {
		t.Errorf("disable: %d %v", code, out)
	}
	if code, st := f.as(t, token, "GET", authBase+"/mfa"); code != 200 || st["required"] != true || st["enabled"] != true {
		t.Errorf("status: %d %v", code, st)
	}
}

// sessionOf signs the enrolled user in through both steps.
func (f *mfaHTTP) sessionOf(t *testing.T) string {
	t.Helper()
	ch := f.challenge(t)
	code, _ := totp.GenerateCode(f.secret, f.svc.Clock())
	rec, out := f.do(t, "POST", authBase+"/mfa/verify", `{"challenge": "`+ch+`", "method": "totp", "code": "`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("verify: %d %v", rec.Code, out)
	}
	return out["token"].(string)
}

func TestEnrollingFromAChallengeOverHTTP(t *testing.T) {
	f := newAuthFixture(t)
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Settings.MFA.RequiredAll = true
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "kim@example.com", "password": "dev-p4ssw0rd!"}`)
	challenge, _ := out["challenge"].(string)
	if rec.Code != 200 || out["mfa_enrollment_required"] != true || challenge == "" {
		t.Fatalf("signup: %d %v", rec.Code, out)
	}
	// The challenge is no session for anything but the two steps of enrolling.
	if code, _ := f.as(t, challenge, "GET", authBase+"/mfa"); code != 401 {
		t.Errorf("challenge as a session: %d", code)
	}
	rec, setup := f.doH(t, "POST", authBase+"/mfa/totp/setup", `{"challenge": "`+challenge+`"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 || setup["secret"] == nil {
		t.Fatalf("setup: %d %v", rec.Code, setup)
	}
	// No challenge and no session: unauthenticated; a body that is not JSON: refused.
	if rec, _ := f.doH(t, "POST", authBase+"/mfa/totp/setup", "", nil); rec.Code != 401 {
		t.Errorf("setup with nothing: %d", rec.Code)
	}
	if rec, out := f.doH(t, "POST", authBase+"/mfa/totp/setup", "challenge=x", map[string]string{"Content-Type": "text/plain"}); rec.Code != 415 {
		t.Errorf("setup with text: %d %v", rec.Code, out)
	}
	code, _ := totp.GenerateCode(setup["secret"].(string), f.svc.Clock())
	body := `{"challenge": "` + challenge + `", "setup_id": "` + setup["setup_id"].(string) + `", "code": "` + code + `"}`
	rec, done := f.doH(t, "POST", authBase+"/mfa/totp/confirm", body, map[string]string{"Content-Type": "application/json"})
	token, _ := done["token"].(string)
	codes, _ := done["recovery_codes"].([]any)
	if rec.Code != 200 || !strings.HasPrefix(token, "bds_") || len(codes) != 10 || done["user"] == nil {
		t.Fatalf("confirm: %d %v", rec.Code, done)
	}
	if c, me := f.as(t, token, "GET", authBase+"/me"); c != 200 || me["email"] != "kim@example.com" {
		t.Errorf("me: %d %v", c, me)
	}
}
