package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// ssoOrg is the organization of newOrgSignIn with two people: ana, who comes in through its Google
// connection and manages it (and holds a password, to prove herself), and ada, who signs in to it
// with her password.
type ssoOrg struct {
	*orgSignIn
	anaTok, anaID, adaTok string
}

func newSSOOrg(t *testing.T) *ssoOrg {
	t.Helper()
	o := newOrgSignIn(t)
	ctx := context.Background()
	_, out := o.redeem(t, o.signInOrg(t, "google", orgGoogle("g-ana", "ana@acme.example", "acme.example", true)))
	s := &ssoOrg{orgSignIn: o, anaTok: out["token"].(string), anaID: out["user"].(map[string]any)["id"].(string)}
	if _, err := o.svc.AdminSetMemberRoles(ctx, o.org, s.anaID, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	// Setting a password ends her sessions: she comes in through the connection again.
	if err := o.svc.SetPassword(ctx, "ana@acme.example", "dev-p4ssw0rd!"); err != nil {
		t.Fatal(err)
	}
	_, out = o.redeem(t, o.signInOrg(t, "google", orgGoogle("g-ana", "ana@acme.example", "acme.example", true)))
	s.anaTok = out["token"].(string)
	s.adaTok = o.sessionIn(t, o.ada, o.org)
	return s
}

func TestASessionRecordsThatAConnectionOpenedIt(t *testing.T) {
	s := newSSOOrg(t)
	p := mustPrincipal(t, s.rulesFixture, s.anaTok)
	if org, provider, ok := p.Session.Connection(); !ok || org != s.org || provider != "google" {
		t.Errorf("connection session = %v %v %v (methods %v)", org, provider, ok, p.Session.Methods)
	}
	if _, _, ok := mustPrincipal(t, s.rulesFixture, s.adaTok).Session.Connection(); ok {
		t.Error("a password session says a connection opened it")
	}
}

func TestRequiringTheOrganizationsConnection(t *testing.T) {
	s := newSSOOrg(t)
	org, path := s.org, orgsPath+"/"+s.org
	patch := func(tok, body string) (int, map[string]any) {
		rec, out := s.fixture.doH(t, "PATCH", path, body, s.withProof(t, tok))
		return rec.Code, out
	}

	// Nothing to require before a person has signed in through the connection: the owner who signed
	// in with her password can't lock everybody out.
	if code, out := patch(s.adaTok, `{"sso_required": true}`); code != 409 || errCode(out) != "sso_not_ready" {
		t.Errorf("without a connection sign-in: %d %v", code, out)
	}
	// Ana did, and may.
	code, out := patch(s.anaTok, `{"sso_required": true}`)
	if code != 200 || out["sso_required"] != true || out["members_without_sso"] != float64(1) || out["sessions_revoked"] != float64(1) {
		t.Fatalf("turn it on: %d %v", code, out)
	}
	// The session that did not come through the connection ended; ana's stays.
	if rec, _ := s.fixture.doH(t, "GET", path, "", bearer(s.adaTok)); rec.Code != 401 {
		t.Errorf("a password session after: %d", rec.Code)
	}
	if rec, _ := s.fixture.doH(t, "GET", path, "", bearer(s.anaTok)); rec.Code != 200 {
		t.Errorf("the connection's session after: %d", rec.Code)
	}
	if got := s.auditActions(t, "org.sso_required"); len(got) != 1 {
		t.Errorf("audit = %v", got)
	}

	// The other ways in answer 400 sso_required, to anyone.
	base := orgAuthPath + "/" + org
	for name, c := range map[string]struct{ path, body string }{
		"password":       {base + "/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`},
		"password, slug": {orgAuthPath + "/acme-corp/login", `{"email": "nobody@example.com", "password": "x"}`},
		"sign-up":        {base + "/signup", `{"email": "new@example.com", "password": "dev-p4ssw0rd!", "invitation": "bdo_x"}`},
		"magic link":     {base + "/magic-link", `{"email": "ada@example.com"}`},
		"reset":          {base + "/reset-password/request", `{"email": "ada@example.com"}`},
	} {
		if rec, out := s.fixture.doH(t, "POST", c.path, c.body, anon); rec.Code != 400 || errCode(out) != "sso_required" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// An unknown organization still looks like a wrong password.
	if rec, out := s.fixture.doH(t, "POST", orgAuthPath+"/nosuchcompany/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, anon); rec.Code != 401 || errCode(out) != "invalid_credentials" {
		t.Errorf("an unknown organization: %d %v", rec.Code, out)
	}
	// The realm's own provider is not a way in; the connection's is.
	q := url.Values{"redirect_to": {appRedirect}, "code_challenge": {"x"}}
	if rec, out := s.fixture.doH(t, "GET", base+"/oauth/apple/start?"+q.Encode(), "", nil); rec.Code != 400 || errCode(out) != "sso_required" {
		t.Errorf("a realm provider: %d %v", rec.Code, out)
	}
	if _, code, body, _ := s.start(t, s.base, "google"); code != 302 {
		t.Errorf("the connection: %d %s", code, body)
	}
	// providers offers only the connection.
	rec, list := s.fixture.doH(t, "GET", base+"/providers", "", nil)
	if rec.Code != 200 || list["sso_required"] != true || len(list["methods"].([]any)) != 0 || strings.Join(strs(list["items"]), ",") != "google,microsoft" {
		t.Errorf("providers: %d %v", rec.Code, list)
	}
	// The members who can't come in are listed to the managers.
	if rec, out := s.fixture.doH(t, "GET", path+"/members?sso=false", "", bearer(s.anaTok)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("members without sso: %d %v", rec.Code, out)
	}
	if rec, _ := s.fixture.doH(t, "GET", path+"/members?sso=true", "", bearer(s.anaTok)); rec.Code != 400 {
		t.Errorf("sso=true: %d", rec.Code)
	}

	// Break glass: an administrator clears it; then the password works again.
	s.bobHolds(t, "staff")
	admin := jsonHdr(s.bob)
	if rec, out := s.fixture.doH(t, "PATCH", "/v1/acme/_admin/orgs/"+org, `{"sso_required": true}`, admin); rec.Code != 409 {
		t.Errorf("an administrator turning it on: %d %v", rec.Code, out)
	}
	if rec, out := s.fixture.doH(t, "PATCH", "/v1/acme/_admin/orgs/"+org, `{"sso_required": false}`, admin); rec.Code != 200 || out["sso_required"] != false {
		t.Fatalf("clear: %d %v", rec.Code, out)
	}
	if rec, out := s.fixture.doH(t, "POST", base+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`, anon); rec.Code != 200 {
		t.Errorf("password after it was cleared: %d %v", rec.Code, out)
	}
}

// Turning it off needs neither a connection nor having signed in through one; and a requirement
// whose last connection goes away is released, or its people would have no way in.
func TestTheRequirementEndsWithTheLastConnection(t *testing.T) {
	s := newSSOOrg(t)
	ctx := context.Background()
	if code, out := func() (int, map[string]any) {
		rec, out := s.fixture.doH(t, "PATCH", orgsPath+"/"+s.org, `{"sso_required": true}`, s.withProof(t, s.anaTok))
		return rec.Code, out
	}(); code != 200 {
		t.Fatalf("turn it on: %d %v", code, out)
	}
	if err := s.svc.AdminDeleteOrgConnection(ctx, s.org, "google"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.svc.Store.OrgByID(ctx, s.org); !o.SSORequired {
		t.Error("released with a connection left")
	}
	if err := s.svc.AdminDeleteOrgConnection(ctx, s.org, "microsoft"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.svc.Store.OrgByID(ctx, s.org); o.SSORequired {
		t.Error("still required with no connection")
	}
}

// A session a connection opened proves who the person is there, not that the account is theirs.
func TestAConnectionSessionCannotChangeTheAccount(t *testing.T) {
	s := newSSOOrg(t)
	s.svc.Settings.Account.AllowEmailChange = true
	h := s.withProof(t, s.anaTok)
	for name, c := range map[string]struct{ method, path, body string }{
		"delete the account":     {"DELETE", "/v1/acme/_auth/me", ""},
		"change the password":    {"POST", "/v1/acme/_auth/password", `{"new_password": "dev-p4ssw0rd!2"}`},
		"remove the password":    {"DELETE", "/v1/acme/_auth/password", ""},
		"unlink a provider":      {"DELETE", "/v1/acme/_auth/identities/google?org=" + s.org, ""},
		"set up a second factor": {"POST", "/v1/acme/_auth/mfa/totp/setup", ""},
		"change the email":       {"POST", "/v1/acme/_auth/email", `{"new_email": "other@acme.example"}`},
	} {
		if rec, out := s.fixture.doH(t, c.method, c.path, c.body, h); rec.Code != 403 || errCode(out) != "sso_session" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// Managing the organization still works.
	if rec, out := s.fixture.doH(t, "PATCH", orgsPath+"/"+s.org, `{"name": "Acme Inc"}`, h); rec.Code != 200 {
		t.Errorf("rename: %d %v", rec.Code, out)
	}
	// The same person with a session of their own credentials can: ana's password session in the org.
	own, err := s.svc.SignInToOrg(context.Background(), auth.OrgSignIn{Org: s.org, Email: "ana@acme.example", Password: "dev-p4ssw0rd!"})
	if err != nil || own.Token == "" {
		t.Fatalf("a password session: %v", err)
	}
	if rec, out := s.fixture.doH(t, "POST", "/v1/acme/_auth/password", `{"new_password": "dev-p4ssw0rd!2"}`, s.withProof(t, own.Token)); rec.Code == 403 {
		t.Errorf("a password session was refused: %d %v", rec.Code, out)
	}
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
