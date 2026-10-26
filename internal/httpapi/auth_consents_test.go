package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// consentFixture is the auth fixture of a realm with a required consent whose current
// version asks for re-acceptance by 2026-11-15, and an optional one.
func consentFixture(t *testing.T) *authFixture {
	t.Helper()
	f := newAuthFixture(t)
	text := func(body string) map[string]registry.ConsentText {
		return map[string]registry.ConsentText{"en": {Body: body, Hash: strings.Repeat("a", 64)}}
	}
	f.svc.Now = func() time.Time { return *f.clock }
	f.svc.Settings.IdleTimeout, f.svc.Settings.MaxLifetime = 90*24*time.Hour, 365*24*time.Hour
	f.svc.Settings.Consents = map[string]*registry.Consent{
		"terms": {
			Name: "terms", Required: true, Current: "2",
			Versions: map[string]*registry.ConsentVersion{
				"1": {},
				"2": {Reaccept: true, ReacceptBy: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)},
			},
			Texts: map[string]map[string]registry.ConsentText{"1": text("old"), "2": text("new")},
		},
		"marketing": {
			Name: "marketing", Current: "1",
			Versions: map[string]*registry.ConsentVersion{"1": {}},
			Texts:    map[string]map[string]registry.ConsentText{"1": text("news")},
		},
	}
	return f
}

func TestConsentsAtSignup(t *testing.T) {
	f := consentFixture(t)
	signup := func(consents string) (*httpRec, map[string]any) {
		body := `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"` + consents + `}`
		rec, out := f.do(t, "POST", authBase+"/signup", body)
		return &httpRec{rec.Code}, out
	}

	// Missing, outdated or unknown consents: 400 with what is needed.
	for name, tc := range map[string]struct{ consents, code, path string }{
		"missing":    {``, "consent_required", "consents.terms"},
		"outdated":   {`, "consents": {"terms": "1"}`, "consent_required", "consents.terms"},
		"only extra": {`, "consents": {"marketing": "1"}`, "consent_required", "consents.terms"},
		"unknown":    {`, "consents": {"terms": "2", "cookies": "1"}`, "validation_error", "consents.cookies"},
		"no text":    {`, "consents": {"terms": "9"}`, "validation_error", "consents.terms"},
		"not object": {`, "consents": ["terms"]`, "validation_error", "consents"},
		"not string": {`, "consents": {"terms": 2}`, "validation_error", "consents.terms"},
	} {
		rec, out := signup(tc.consents)
		_, details := errorOf(out)
		if rec.code != http.StatusBadRequest || errCode(out) != tc.code || len(details) == 0 || details[0].(map[string]any)["path"] != tc.path {
			t.Errorf("%s: %d %v", name, rec.code, out)
		}
	}
	if users, _ := f.store.ListUsers(t.Context()); len(users) != 0 {
		t.Errorf("a refused sign-up made an account")
	}

	// Accepted: the account, its records and its summary.
	rec, out := signup(`, "consents": {"terms": "2", "marketing": "1"}`)
	if rec.code != http.StatusCreated {
		t.Fatalf("signup = %d %v", rec.code, out)
	}
	uid := out["user"].(map[string]any)["id"].(string)
	hist, _ := f.svc.ConsentHistory(t.Context(), uid)
	if len(hist) != 2 || hist[0].Method != "signup" || hist[0].Consent != "marketing" || hist[1].Consent != "terms" || hist[1].Version != "2" {
		t.Errorf("history = %+v", hist)
	}
	me, _ := f.doH(t, "GET", authBase+"/me", "", bearer(out["token"].(string)))
	body := decodeBody(me)
	if c := body["consents"].(map[string]any); c["terms"] != "2" || c["marketing"] != "1" || len(body["to_accept"].([]any)) != 0 {
		t.Errorf("me = %v", body)
	}
}

type httpRec struct{ code int }

// signupWith signs a user up with extra members in the body (consents) and returns the token.
func (f *authFixture) signupWith(t *testing.T, email, extra string) (string, map[string]any) {
	t.Helper()
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"`+extra+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %s: %d %v", email, rec.Code, out)
	}
	return out["token"].(string), out
}

func TestConsentChallengeAtSignIn(t *testing.T) {
	f := consentFixture(t)
	// An account from before the consent existed (nothing accepted).
	if _, err := f.svc.Create(t.Context(), "old@example.com", new("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	rec, out := f.do(t, "POST", authBase+"/login", `{"email": "old@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusOK || out["consent_required"] != true || out["token"] != nil {
		t.Fatalf("login = %d %v", rec.Code, out)
	}
	challenge := out["challenge"].(string)
	list := out["consents"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "terms" || list[0].(map[string]any)["version"] != "2" {
		t.Errorf("consents = %v", list)
	}

	// Not accepting everything, or a wrong version, keeps the challenge.
	for _, body := range []string{
		`{"challenge": "` + challenge + `", "consents": {"marketing": "1"}}`,
		`{"challenge": "` + challenge + `", "consents": {"terms": "1"}}`,
	} {
		rec, out := f.do(t, "POST", authBase+"/consents/accept", body)
		if rec.Code != http.StatusBadRequest || errCode(out) != "consent_required" {
			t.Errorf("%s = %d %v", body, rec.Code, out)
		}
	}
	if rec, out := f.do(t, "POST", authBase+"/consents/accept", `{"challenge": "nope", "consents": {"terms": "2"}}`); rec.Code != http.StatusBadRequest || errCode(out) != "invalid_challenge" {
		t.Errorf("unknown challenge = %d %v", rec.Code, out)
	}

	rec, out = f.do(t, "POST", authBase+"/consents/accept", `{"challenge": "`+challenge+`", "consents": {"terms": "2", "marketing": "1"}}`)
	if rec.Code != http.StatusOK || !strings.HasPrefix(out["token"].(string), "bds_") {
		t.Fatalf("accept = %d %v", rec.Code, out)
	}
	uid := out["user"].(map[string]any)["id"].(string)
	hist, _ := f.svc.ConsentHistory(t.Context(), uid)
	if len(hist) != 2 || hist[0].Method != "prompt" {
		t.Errorf("history = %+v", hist)
	}
	// One use.
	if rec, _ := f.do(t, "POST", authBase+"/consents/accept", `{"challenge": "`+challenge+`", "consents": {"terms": "2"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("challenge reused = %d", rec.Code)
	}
	// And the next sign-in has nothing to ask.
	rec, out = f.do(t, "POST", authBase+"/login", `{"email": "old@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusOK || out["token"] == nil {
		t.Errorf("second login = %d %v", rec.Code, out)
	}
}

func TestConsentDeadlineRefusesOpenSessions(t *testing.T) {
	f := consentFixture(t)
	// A session that exists since before the consent was asked for.
	token, out := f.signupWith(t, "ada@example.com", `, "consents": {"terms": "2"}`)
	// …as if they had accepted the previous version, before the new one asked for it.
	uid := out["user"].(map[string]any)["id"].(string)
	if err := f.store.SetUserConsent(t.Context(), uid, "terms", "1"); err != nil {
		t.Fatal(err)
	}
	// Terms 1 is not current, but nothing refuses it before the date.
	if rec, _ := f.doH(t, "GET", authBase+"/sessions", "", bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("before the deadline = %d", rec.Code)
	}
	*f.clock = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	rec, _ := f.doH(t, "GET", authBase+"/sessions", "", bearer(token))
	body := decodeBody(rec)
	if rec.Code != http.StatusForbidden || errCode(body) != "consent_required" {
		t.Fatalf("after the deadline = %d %v", rec.Code, body)
	}
	// What stays open: who they are, signing out and accepting.
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(token)); rec.Code != http.StatusOK {
		t.Errorf("me = %d", rec.Code)
	}
	rec, _ = f.doH(t, "POST", authBase+"/consents", `{"consents": {"marketing": "1"}}`, bearer(token))
	if rec.Code != http.StatusOK || decodeBody(rec)["to_accept"].([]any)[0].(map[string]any)["name"] != "terms" {
		t.Errorf("accepting only an optional one = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/sessions", "", bearer(token)); rec.Code != http.StatusForbidden {
		t.Errorf("still missing = %d", rec.Code)
	}
	rec, _ = f.doH(t, "POST", authBase+"/consents", `{"consents": {"terms": "2"}}`, bearer(token))
	if rec.Code != http.StatusOK || len(decodeBody(rec)["to_accept"].([]any)) != 0 {
		t.Errorf("accepting terms = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/sessions", "", bearer(token)); rec.Code != http.StatusOK {
		t.Errorf("after accepting = %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", authBase+"/consents", `{"consents": {}}`, bearer(token)); rec.Code != http.StatusBadRequest {
		t.Errorf("empty = %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", authBase+"/logout", "", bearer(token)); rec.Code != http.StatusNoContent {
		t.Errorf("logout = %d", rec.Code)
	}
}

// A person who won't accept a new version can still leave: deleting the account is not
// behind the consent they are refusing.
func TestConsentGateLeavesTheAccountDeletionOpen(t *testing.T) {
	f := consentFixture(t)
	token, out := f.signupWith(t, "ada@example.com", `, "consents": {"terms": "2"}`)
	if err := f.store.SetUserConsent(t.Context(), out["user"].(map[string]any)["id"].(string), "terms", "1"); err != nil {
		t.Fatal(err)
	}
	*f.clock = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	if rec, _ := f.doH(t, "GET", authBase+"/sessions", "", bearer(token)); rec.Code != http.StatusForbidden {
		t.Fatalf("the gate is not closed: %d", rec.Code)
	}
	body := `{"password": "dev-p4ssw0rd!"}`
	rec, _ := f.doH(t, "DELETE", authBase+"/me", body, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
	if rec.Code == http.StatusForbidden && errCode(decodeBody(rec)) == "consent_required" {
		t.Errorf("deleting the account is refused by the consent gate: %s", rec.Body)
	}
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Logf("delete answered %d %s (anything but the consent gate is fine here)", rec.Code, rec.Body)
	}
}

func TestConsentWithdrawalAndHistory(t *testing.T) {
	f := consentFixture(t)
	token, _ := f.signupWith(t, "ada@example.com", `, "consents": {"terms": "2", "marketing": "1"}`)
	auth := bearer(token)

	rec, _ := f.doH(t, "GET", authBase+"/consents", "", auth)
	body := decodeBody(rec)
	hist := body["history"].([]any)
	if rec.Code != http.StatusOK || body["consents"].(map[string]any)["marketing"] != "1" || len(hist) != 2 ||
		hist[0].(map[string]any)["method"] != "signup" || hist[0].(map[string]any)["text_hash"] != nil {
		t.Fatalf("consents = %d %v", rec.Code, body)
	}

	// An optional consent is withdrawn as easily as it was given, and again changes nothing.
	for range 2 {
		rec, _ = f.doH(t, "DELETE", authBase+"/consents/marketing", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("withdraw = %d %s", rec.Code, rec.Body)
		}
		if _, held := decodeBody(rec)["consents"].(map[string]any)["marketing"]; held {
			t.Errorf("still held: %s", rec.Body)
		}
	}
	rec, _ = f.doH(t, "GET", authBase+"/consents", "", auth)
	hist = decodeBody(rec)["history"].([]any)
	if len(hist) != 3 || hist[2].(map[string]any)["action"] != "withdrawn" || hist[2].(map[string]any)["version"] != "1" {
		t.Errorf("history after withdrawal = %v", hist)
	}

	// A required one is not withdrawn: the way out is deleting the account.
	rec, out := f.doH(t, "DELETE", authBase+"/consents/terms", "", auth)
	if rec.Code != http.StatusConflict || errCode(decodeBody(rec)) != "consent_required_for_account" {
		t.Errorf("withdraw required = %d %v %s", rec.Code, out, rec.Body)
	}
	if rec, _ = f.doH(t, "DELETE", authBase+"/consents/nope", "", auth); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d", rec.Code)
	}
}

// The recovery codes of an enrolment are shown once: when a consent step follows, they
// travel with it.
func TestEnrolmentKeepsTheRecoveryCodesWhenConsentsFollow(t *testing.T) {
	f := consentFixture(t)
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Settings.MFA.RequiredAll = true
	if _, err := f.svc.Create(t.Context(), "root@example.com", new("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	rec, out := f.do(t, "POST", authBase+"/login", `{"email": "root@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusOK || out["mfa_enrollment_required"] != true {
		t.Fatalf("login = %d %v", rec.Code, out)
	}
	challenge := out["challenge"].(string)
	rec, out = f.do(t, "POST", authBase+"/mfa/totp/setup", `{"challenge": "`+challenge+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d %v", rec.Code, out)
	}
	code, err := totp.GenerateCode(out["secret"].(string), f.svc.Clock())
	if err != nil {
		t.Fatal(err)
	}
	rec, out = f.do(t, "POST", authBase+"/mfa/totp/confirm", `{"challenge": "`+challenge+`", "setup_id": "`+out["setup_id"].(string)+`", "code": "`+code+`"}`)
	if rec.Code != http.StatusOK || out["consent_required"] != true || len(out["recovery_codes"].([]any)) != 10 || out["token"] != nil {
		t.Fatalf("confirm = %d %v", rec.Code, out)
	}
	// The consent step then ends the sign-in.
	rec, out = f.do(t, "POST", authBase+"/consents/accept", `{"challenge": "`+out["challenge"].(string)+`", "consents": {"terms": "2"}}`)
	if rec.Code != http.StatusOK || out["token"] == nil {
		t.Fatalf("accept = %d %v", rec.Code, out)
	}
}
