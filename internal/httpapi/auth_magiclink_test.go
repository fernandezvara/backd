package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

const magicPath = "/v1/acme/_auth/magic-link"

func newMagicFixture(t *testing.T) (*rulesFixture, *Worker) {
	t.Helper()
	f, w := newVerifyFixture(t)
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true}
	f.svc.Settings.SignInRedirects = []string{"https://app.acme.example", "acme://"}
	f.svc.Settings.SignInAppURL = "https://app.acme.example/signed-in"
	return f, w
}

func (f *rulesFixture) askMagic(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	rec, out := f.doH(t, "POST", magicPath, body, map[string]string{"Content-Type": "application/json"})
	return rec.Code, out
}

func TestMagicLinkOverHTTP(t *testing.T) {
	f, w := newMagicFixture(t)
	verifier, challenge, _ := auth.NewPKCE()
	code, out := f.askMagic(t, `{"email": "ada@example.com", "redirect_to": "https://app.acme.example/account?tab=2", "code_challenge": "`+challenge+`"}`)
	requestID, _ := out["request_id"].(string)
	if code != http.StatusAccepted || requestID == "" {
		t.Fatalf("request: %d %v", code, out)
	}
	in := deliver(t, f, w)
	if in.Kind != "magic-link" || in.To[0].Email != "ada@example.com" {
		t.Fatalf("message: %+v", in)
	}
	token, typed := tokenOf(t, in), in.Data["code"].(string)
	if len(typed) != 6 || !strings.Contains(in.Text, typed) || !strings.Contains(in.Text, "/_auth/magic-link?token="+token) {
		t.Errorf("message text: %q", in.Text)
	}

	// Opening the link (a scanner does) shows the button and uses nothing up.
	rec := f.page(t, "GET", "/v1/acme/_auth/magic-link?token="+token, "", "", "")
	checkPageHeaders(t, rec)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="token" value="`+token+`"`) || !strings.Contains(rec.Body.String(), `action="/v1/acme/_auth/magic-link"`) {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if rec := f.page(t, "GET", "/v1/acme/_auth/magic-link?token=nope", "", "", ""); rec.Code != 400 {
		t.Errorf("an unknown link: %d", rec.Code)
	}
	// The button signs in: the browser goes back to the page it came from with a one-time code, never a session.
	rec = f.page(t, "POST", magicPath, formType, url.Values{"token": {token}}.Encode(), "")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || loc.Host != "app.acme.example" || loc.Path != "/account" || loc.Query().Get("tab") != "2" || loc.Query().Get("backd_code") == "" {
		t.Fatalf("use: %d %v", rec.Code, loc)
	}
	if strings.Contains(rec.Header().Get("Location"), "bds_") {
		t.Error("a session in the address")
	}
	rec2, session := f.doH(t, "POST", "/v1/acme/_auth/token", `{"code": "`+loc.Query().Get("backd_code")+`", "code_verifier": "`+verifier+`"}`, map[string]string{"Content-Type": "application/json"})
	if rec2.Code != 200 || session["token"] == nil || session["new_user"] != false {
		t.Fatalf("redeem: %d %v", rec2.Code, session)
	}
	if rec := f.page(t, "POST", magicPath, formType, url.Values{"token": {token}}.Encode(), ""); rec.Code != 400 {
		t.Errorf("the link twice: %d", rec.Code)
	}
}

func TestMagicLinkCodeOverHTTP(t *testing.T) {
	f, w := newMagicFixture(t)
	verifier, challenge, _ := auth.NewPKCE()
	_, out := f.askMagic(t, `{"email": "ada@example.com", "code_challenge": "`+challenge+`"}`)
	in := deliver(t, f, w)
	typed := in.Data["code"].(string)
	id := out["request_id"].(string)
	post := func(body string) (int, map[string]any) {
		rec, o := f.doH(t, "POST", magicPath+"/verify", body, map[string]string{"Content-Type": "application/json"})
		return rec.Code, o
	}
	if code, o := post(`{"request_id": "` + id + `", "code": "000000", "code_verifier": "` + verifier + `"}`); code != 400 || errCode(o) != "invalid_code" {
		t.Errorf("wrong code: %d %v", code, o)
	}
	if code, o := post(`{"request_id": "` + id + `", "code": "` + typed + `"}`); code != 400 || errCode(o) != "validation_error" {
		t.Errorf("no verifier: %d %v", code, o)
	}
	code, o := post(`{"request_id": "` + id + `", "code": "` + typed + `", "code_verifier": "` + verifier + `"}`)
	if code != 200 || o["token"] == nil || o["new_user"] != false {
		t.Fatalf("verify: %d %v", code, o)
	}
	if code, o := post(`{"request_id": "` + id + `", "code": "` + typed + `", "code_verifier": "` + verifier + `"}`); code != 400 || errCode(o) != "invalid_magic_link" {
		t.Errorf("again: %d %v", code, o)
	}
}

func TestMagicLinkRequestRefusals(t *testing.T) {
	f, _ := newMagicFixture(t)
	_, challenge, _ := auth.NewPKCE()
	cases := map[string]struct {
		body string
		want int
		code string
	}{
		"an invalid address":       {`{"email": "nope", "code_challenge": "` + challenge + `"}`, 400, "validation_error"},
		"no challenge":             {`{"email": "ada@example.com"}`, 400, "validation_error"},
		"a short challenge":        {`{"email": "ada@example.com", "code_challenge": "abc"}`, 400, "validation_error"},
		"a foreign redirect":       {`{"email": "ada@example.com", "code_challenge": "` + challenge + `", "redirect_to": "https://evil.example/"}`, 400, "invalid_redirect"},
		"an unknown field":         {`{"email": "ada@example.com", "code_challenge": "` + challenge + `", "role": "admin"}`, 400, "validation_error"},
		"an app scheme is allowed": {`{"email": "ada@example.com", "code_challenge": "` + challenge + `", "redirect_to": "acme://signed-in"}`, 202, ""},
	}
	for name, c := range cases {
		code, out := f.askMagic(t, c.body)
		if code != c.want || (c.code != "" && errCode(out) != c.code) {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// Not JSON and not a form: refused.
	if rec, _ := f.doH(t, "POST", magicPath, "email=a", map[string]string{"Content-Type": "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text: %d", rec.Code)
	}
	f.svc.Settings.MagicLink.Enabled = false
	if code, out := f.askMagic(t, `{"email": "ada@example.com", "code_challenge": "`+challenge+`"}`); code != 403 || errCode(out) != "magic_link_disabled" {
		t.Errorf("disabled: %d %v", code, out)
	}
	if rec := f.page(t, "GET", magicPath+"?token=x", "", "", ""); rec.Code != 404 {
		t.Errorf("page when disabled: %d", rec.Code)
	}
}

func TestMagicLinkForANewcomerAndAMissingDestination(t *testing.T) {
	f, w := newMagicFixture(t)
	f.svc.Settings.MagicLink.Signup = true
	verifier, challenge, _ := auth.NewPKCE()
	_, out := f.askMagic(t, `{"email": "Fresh@Example.com", "code_challenge": "`+challenge+`"}`)
	in := deliver(t, f, w)
	if in.Kind != "magic-link" || in.To[0].Email != "fresh@example.com" {
		t.Fatalf("message: %+v", in)
	}
	if _, err := f.svc.Find(t.Context(), "fresh@example.com"); err == nil {
		t.Fatal("the account exists before the link was used")
	}
	code, o := f.doH(t, "POST", magicPath+"/verify", `{"request_id": "`+out["request_id"].(string)+`", "code": "`+in.Data["code"].(string)+`", "code_verifier": "`+verifier+`"}`, map[string]string{"Content-Type": "application/json"})
	if code.Code != 200 || o["new_user"] != true {
		t.Fatalf("newcomer: %d %v", code.Code, o)
	}
	if u, err := f.svc.Find(t.Context(), "fresh@example.com"); err != nil || !u.EmailVerified {
		t.Errorf("account: %+v %v", u, err)
	}

	// A request that has nowhere to go: the page says so and the link is not used up.
	f.svc.Settings.SignInAppURL = ""
	if code, out := f.askMagic(t, `{"email": "ada@example.com", "code_challenge": "`+challenge+`"}`); code != 202 {
		t.Fatalf("request: %d %v", code, out)
	}
	in = deliver(t, f, w)
	token := tokenOf(t, in)
	rec := f.page(t, "GET", magicPath+"?token="+token, "", "", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `name="token"`) || !strings.Contains(rec.Body.String(), "type the code") {
		t.Errorf("page with nowhere to go: %d %s", rec.Code, rec.Body)
	}
	rec = f.page(t, "POST", magicPath, formType, url.Values{"token": {token}}.Encode(), "")
	if rec.Code != 200 || rec.Header().Get("Location") != "" {
		t.Errorf("post with nowhere to go: %d %v", rec.Code, rec.Header())
	}
	// The code still works.
	if _, err := f.svc.VerifyMagicLinkCode(t.Context(), "x", "1", "v", ""); err == nil {
		t.Error("an unknown request was accepted")
	}
}

// Someone who only ever signs in with links proves who they are with an emailed code, sets a
// password, and can drop it again.
func TestPasswordlessAccountLifecycle(t *testing.T) {
	f, w := newMagicFixture(t)
	f.svc.Settings.MagicLink.Signup = true
	verifier, challenge, _ := auth.NewPKCE()
	_, out := f.askMagic(t, `{"email": "kim@example.com", "code_challenge": "`+challenge+`"}`)
	in := deliver(t, f, w)
	rec, o := f.doH(t, "POST", magicPath+"/verify", `{"request_id": "`+out["request_id"].(string)+`", "code": "`+in.Data["code"].(string)+`", "code_verifier": "`+verifier+`"}`, map[string]string{"Content-Type": "application/json"})
	token, _ := o["token"].(string)
	if rec.Code != 200 || o["new_user"] != true || token == "" {
		t.Fatalf("sign-in: %d %v", rec.Code, o)
	}
	code, me := f.as(t, token, "GET", "/v1/acme/_auth/me", "")
	if code != 200 || len(me["sign_in_methods"].([]any)) != 1 || me["sign_in_methods"].([]any)[0] != "magic_link" || me["reauth_methods"].([]any)[0] != "email_code" {
		t.Fatalf("me: %d %v", code, me)
	}
	ids := me["identities"].([]any)
	if len(ids) != 1 || ids[0].(map[string]any)["provider"] != "email" || ids[0].(map[string]any)["email"] != "kim@example.com" {
		t.Errorf("identities: %v", ids)
	}
	// The last way in can't go, and the sensitive actions ask for the emailed code.
	hdr := jsonHdr(token)
	if rec, o := f.doH(t, "POST", "/v1/acme/_auth/password", `{"new_password": "dev-p4ssw0rd!7"}`, hdr); rec.Code != 401 || errCode(o) != "reauth_required" {
		t.Fatalf("a password without a proof: %d %v", rec.Code, o)
	}
	if rec, o := f.doH(t, "POST", "/v1/acme/_auth/reauth/email-code", "", hdr); rec.Code != 202 {
		t.Fatalf("ask for a code: %d %v", rec.Code, o)
	}
	mail := deliver(t, f, w)
	if mail.Kind != "reauth-code" || mail.To[0].Email != "kim@example.com" {
		t.Fatalf("message: %+v", mail)
	}
	rec, o = f.doH(t, "POST", "/v1/acme/_auth/reauth", `{"method": "email_code", "code": "`+mail.Data["code"].(string)+`"}`, hdr)
	proof, _ := o["reauth_token"].(string)
	if rec.Code != 200 || proof == "" {
		t.Fatalf("reauth: %d %v", rec.Code, o)
	}
	hdr[reauthHeader] = proof
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/password", `{"new_password": "dev-p4ssw0rd!7"}`, hdr); rec.Code != 204 {
		t.Fatalf("set a password: %d", rec.Code)
	}
	if rec, o := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "kim@example.com", "password": "dev-p4ssw0rd!7"}`, nil); rec.Code != 200 || o["token"] == nil {
		t.Errorf("login with the new password: %d %v", rec.Code, o)
	}
	// With the password set, the proof is the password from now on; removing it is allowed
	// while magic links remain.
	if rec, o := f.doH(t, "DELETE", "/v1/acme/_auth/password", "", hdr); rec.Code != 204 {
		t.Errorf("remove the password: %d %v", rec.Code, o)
	}
	if rec, o := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "kim@example.com", "password": "dev-p4ssw0rd!7"}`, nil); rec.Code != 401 {
		t.Errorf("login with the removed password: %d %v", rec.Code, o)
	}
	// And when links are off the user has no way left to remove.
	f.svc.Settings.MagicLink.Enabled = false
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/password", `{"new_password": "dev-p4ssw0rd!8"}`, hdr); rec.Code != 204 {
		t.Fatalf("set again: %d", rec.Code)
	}
	if rec, o := f.doH(t, "DELETE", "/v1/acme/_auth/password", "", hdr); rec.Code != 409 || errCode(o) != "last_sign_in_method" {
		t.Errorf("the only way in: %d %v", rec.Code, o)
	}
}
