package httpapi

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

const orgAuthPath = "/v1/acme/_auth/orgs"

const testPassword = "dev-p4ssw0rd!"

// anon is the header of a JSON request with no session.
var anon = map[string]string{"Content-Type": "application/json"}

// scopeOf returns the organization a session token is scoped to.
func (f *rulesFixture) scopeOf(t *testing.T, token string) string {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p.Session.Org
}

// newOrgSignInFixture is a realm with a company Ada owns (slug acme-corp) and Bob does not belong to.
func newOrgSignInFixture(t *testing.T) (*rulesFixture, *Worker, string) {
	t.Helper()
	f, w := newMagicFixture(t)
	f.svc.Settings.Organizations.SigninLink = "https://app.acme.example/{slug}/signin"
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "Acme Corp", "slug": "acme-corp"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	return f, w, out["id"].(string)
}

func (f *rulesFixture) orgLogin(t *testing.T, org, email, password, extra string) (int, map[string]any) {
	t.Helper()
	body := `{"email": "` + email + `", "password": "` + password + `"` + extra + `}`
	rec, out := f.doH(t, "POST", orgAuthPath+"/"+org+"/login", body, anon)
	return rec.Code, out
}

var requestIDPattern = regexp.MustCompile(`"request_id":"[^"]*"`)

// Signing in to an organization gives a session scoped to it, by id or by slug; everything that
// keeps a person out answers exactly like a wrong password.
func TestSignInToAnOrganization(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)

	for _, ref := range []string{org, "acme-corp", " ACME-Corp "} {
		code, out := f.orgLogin(t, url.PathEscape(ref), "ada@example.com", testPassword, "")
		if code != 200 || out["token"] == nil {
			t.Fatalf("login to %q: %d %v", ref, code, out)
		}
		if got := f.scopeOf(t, out["token"].(string)); got != org {
			t.Errorf("login to %q: the session is scoped to %q, want %q", ref, got, org)
		}
	}

	// Every way of not getting in is the same answer, with the same body.
	var reference string
	for name, c := range map[string]struct{ org, email, password string }{
		"wrong password":       {org, "ada@example.com", "nope-nope-nope"},
		"a stranger":           {org, "bob@example.com", testPassword},
		"no such address":      {org, "nobody@example.com", testPassword},
		"no such organization": {"nowhere", "ada@example.com", testPassword},
		"an id that is not":    {"0123456789abcdefghij", "ada@example.com", testPassword},
	} {
		code, out := f.orgLogin(t, c.org, c.email, c.password, "")
		if code != 401 || errCode(out) != "invalid_credentials" {
			t.Errorf("%s: %d %v", name, code, out)
		}
		body := requestIDPattern.ReplaceAllString(mustJSON(out), "")
		if reference == "" {
			reference = body
		}
		if body != reference {
			t.Errorf("%s: the answer differs:\n%s\n%s", name, body, reference)
		}
	}

	// Wrong passwords for a member count toward the same limit as anywhere else.
	var limited bool
	for range 12 {
		if code, _ := f.orgLogin(t, org, "ada@example.com", "wrong-wrong-1", ""); code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("repeated failures were never limited")
	}
}

// An invitation is used as part of the sign-in: a person who is not a member yet comes in and joins.
func TestSignInToAnOrganizationWithAnInvitation(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	invite := func(body string) string {
		t.Helper()
		rec, out := f.doH(t, "POST", orgsPath+"/"+org+"/invitations", body, jsonHdr(f.sessionIn(t, f.ada, org)))
		if rec.Code != 201 {
			t.Fatalf("invite: %d %v", rec.Code, out)
		}
		return out["token"].(string)
	}
	token := invite(`{"binding": "email", "email": "bob@example.com", "roles": ["editor"]}`)

	// Without it, Bob is a stranger; with someone else's token he is too.
	if code, _ := f.orgLogin(t, org, "bob@example.com", testPassword, ""); code != 401 {
		t.Fatalf("without the invitation: %d", code)
	}
	if code, _ := f.orgLogin(t, org, "carl@example.com", testPassword, `, "invitation": "`+token+`"`); code != 401 {
		t.Errorf("an invitation of another person: %d", code)
	}
	if code, _ := f.orgLogin(t, org, "bob@example.com", testPassword, `, "invitation": "bdo_nope"`); code != 401 {
		t.Errorf("an invitation that is nothing: %d", code)
	}
	code, out := f.orgLogin(t, org, "bob@example.com", testPassword, `, "invitation": "`+token+`"`)
	if code != 200 || f.scopeOf(t, out["token"].(string)) != org {
		t.Fatalf("with the invitation: %d %v", code, out)
	}
	m, err := f.svc.MembershipOf(context.Background(), f.bobID, org)
	if err != nil || !contains(m.RoleNames(), "editor") {
		t.Errorf("bob did not join as editor: %v %v", m.RoleNames(), err)
	}
	// It is used up, and he is a member now, so he needs no more.
	if code, _ := f.orgLogin(t, org, "bob@example.com", testPassword, `, "invitation": "`+token+`"`); code != 401 {
		t.Errorf("the invitation twice: %d", code)
	}
	if code, _ := f.orgLogin(t, org, "bob@example.com", testPassword, ""); code != 200 {
		t.Errorf("a member signs in: %d", code)
	}
	// An invitation to one organization does not open another.
	other := f.orgOf(t, f.ada, "Other")
	tokenOther := func() string {
		rec, o := f.doH(t, "POST", orgsPath+"/"+other+"/invitations", `{"binding": "none"}`, jsonHdr(f.sessionIn(t, f.ada, other)))
		if rec.Code != 201 {
			t.Fatalf("invite other: %d %v", rec.Code, o)
		}
		return o["token"].(string)
	}()
	if code, _ := f.orgLogin(t, org, "carl@example.com", testPassword, `, "invitation": "`+tokenOther+`"`); code != 401 {
		t.Errorf("an invitation of another organization: %d", code)
	}
}

// A new person joins an organization by invitation; the account is born verified when the
// invitation was sent to its address.
func TestSignUpToAnOrganization(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	invite := func(body string) string {
		rec, out := f.doH(t, "POST", orgsPath+"/"+org+"/invitations", body, jsonHdr(f.sessionIn(t, f.ada, org)))
		if rec.Code != 201 {
			t.Fatalf("invite: %d %v", rec.Code, out)
		}
		return out["token"].(string)
	}
	signup := func(ref, email, token string) (int, map[string]any) {
		rec, out := f.doH(t, "POST", orgAuthPath+"/"+ref+"/signup", `{"email": "`+email+`", "password": "`+testPassword+`", "invitation": "`+token+`"}`, anon)
		return rec.Code, out
	}
	bound := invite(`{"binding": "email", "email": "new@example.com", "roles": ["editor"]}`)

	if code, out := signup("acme-corp", "new@example.com", ""); code != 400 || errCode(out) != "invalid_invitation" {
		t.Errorf("no invitation: %d %v", code, out)
	}
	if code, out := signup("acme-corp", "other@example.com", bound); code != 403 || errCode(out) != "invitation_not_for_you" {
		t.Errorf("another address: %d %v", code, out)
	}
	if code, out := signup("nowhere", "new@example.com", bound); code != 400 || errCode(out) != "invalid_invitation" {
		t.Errorf("another organization: %d %v", code, out)
	}
	if _, err := f.svc.Find(context.Background(), "new@example.com"); err == nil {
		t.Fatal("a refused sign-up made an account")
	}
	code, out := signup("acme-corp", "new@example.com", bound)
	if code != 201 || f.scopeOf(t, out["token"].(string)) != org {
		t.Fatalf("sign-up: %d %v", code, out)
	}
	u, _ := f.svc.Find(context.Background(), "new@example.com")
	if !u.EmailVerified {
		t.Error("an account made from an invitation to its address starts unverified")
	}
	if m, err := f.svc.MembershipOf(context.Background(), u.ID, org); err != nil || !contains(m.RoleNames(), "editor") {
		t.Errorf("membership: %v %v", m.RoleNames(), err)
	}
	// The invitation was for one address and one use.
	if code, _ := signup("acme-corp", "new2@example.com", bound); code != 400 && code != 403 {
		t.Errorf("a used invitation: %d", code)
	}
	// A bound-to-user invitation is not for someone with no account; an open link is for anyone,
	// whose address nobody has vouched for.
	if code, out := signup(org, "n3@example.com", invite(`{"binding": "user", "user": "`+f.bobID+`"}`)); code != 403 {
		t.Errorf("bound to a user: %d %v", code, out)
	}
	code, out = signup(org, "n4@example.com", invite(`{"binding": "none", "roles": ["editor"]}`))
	if code != 201 {
		t.Fatalf("open link: %d %v", code, out)
	}
	if u, _ := f.svc.Find(context.Background(), "n4@example.com"); u.EmailVerified {
		t.Error("an account made from an open link is verified")
	}
	// An address that already has an account is told so.
	if code, out := signup(org, "ada@example.com", invite(`{"binding": "none"}`)); code != 409 {
		t.Errorf("a registered address: %d %v", code, out)
	}
}

// What an invitation offers can be read before signing in, and only by someone who holds the token.
func TestPreviewAnInvitationWithoutASession(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	_, out := f.doH(t, "POST", orgsPath+"/"+org+"/invitations", `{"binding": "email", "email": "bob@example.com", "roles": ["editor"]}`, jsonHdr(f.sessionIn(t, f.ada, org)))
	token := out["token"].(string)
	preview := func(ref, tok string) (int, map[string]any) {
		rec, out := f.doH(t, "POST", orgAuthPath+"/"+ref+"/invitations/preview", `{"token": "`+tok+`"}`, anon)
		return rec.Code, out
	}
	if code, out := preview("acme-corp", token); code != 200 || out["org"].(map[string]any)["name"] != "Acme Corp" || out["roles"].([]any)[0] != "editor" {
		t.Errorf("preview: %d %v", code, out)
	}
	for name, c := range map[string]struct{ ref, tok string }{
		"nonsense": {org, "nope"}, "unknown": {org, "bdo_unknownunknownunknownunknownunknownunknown"},
		"another organization": {f.orgOf(t, f.ada, "Other"), token}, "no organization": {"nowhere", token},
	} {
		if code, out := preview(c.ref, c.tok); code != 400 || errCode(out) != "invalid_invitation" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

// A link by email goes only to members, and the session it gives is scoped to the organization.
func TestMagicLinkToAnOrganization(t *testing.T) {
	f, w, org := newOrgSignInFixture(t)
	verifier, challenge, _ := auth.NewPKCE()
	ask := func(ref, email string) (int, map[string]any) {
		rec, out := f.doH(t, "POST", orgAuthPath+"/"+ref+"/magic-link", `{"email": "`+email+`", "code_challenge": "`+challenge+`"}`, anon)
		return rec.Code, out
	}
	// A stranger, an unknown address and an unknown organization are told nothing: the same answer, no email.
	for name, c := range map[string]struct{ ref, email string }{
		"a stranger": {org, "bob@example.com"}, "no account": {org, "nobody@example.com"}, "no organization": {"nowhere", "ada@example.com"},
	} {
		code, out := ask(c.ref, c.email)
		if code != 202 || out["request_id"] == nil {
			t.Errorf("%s: %d %v", name, code, out)
		}
		if w.RunOnce(context.Background()) {
			t.Errorf("%s: a message was sent", name)
		}
	}
	code, out := ask("acme-corp", "ada@example.com")
	if code != 202 {
		t.Fatalf("ask: %d %v", code, out)
	}
	in := deliver(t, f, w)
	if in.Kind != "magic-link" || in.To[0].Email != "ada@example.com" {
		t.Fatalf("message: %+v", in)
	}
	typed := in.Data["code"].(string)
	rec, session := f.doH(t, "POST", magicPath+"/verify", `{"request_id": "`+out["request_id"].(string)+`", "code": "`+typed+`", "code_verifier": "`+verifier+`"}`, anon)
	if rec.Code != 200 || f.scopeOf(t, session["token"].(string)) != org {
		t.Fatalf("verify: %d %v", rec.Code, session)
	}
}

// A new person starts a company: the account, the organization and a session scoped to it.
func TestANewPersonCreatesAnOrganization(t *testing.T) {
	f, _, _ := newOrgSignInFixture(t)
	create := func(body string) (int, map[string]any) {
		rec, out := f.doH(t, "POST", orgAuthPath, body, anon)
		return rec.Code, out
	}
	const who = `"email": "founder@example.com", "password": "` + testPassword + `"`
	for name, body := range map[string]string{
		"no slug":       `{` + who + `, "name": "Founders"}`,
		"no name":       `{` + who + `, "slug": "founders"}`,
		"a bad slug":    `{` + who + `, "name": "Founders", "slug": "Not A Slug"}`,
		"an id as slug": `{` + who + `, "name": "Founders", "slug": "0123456789abcdefghij"}`,
		"a bad address": `{"email": "nope", "password": "` + testPassword + `", "name": "Founders", "slug": "founders"}`,
	} {
		if code, out := create(body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if _, err := f.svc.Find(context.Background(), "founder@example.com"); err == nil {
		t.Fatal("a refused request made an account")
	}
	// A slug in use is said so, and makes nothing.
	if code, out := create(`{` + who + `, "name": "Again", "slug": "acme-corp"}`); code != 409 || errCode(out) != "slug_taken" {
		t.Errorf("slug taken: %d %v", code, out)
	}
	if _, err := f.svc.Find(context.Background(), "founder@example.com"); err == nil {
		t.Fatal("a refused request made an account")
	}
	code, out := create(`{` + who + `, "name": "Founders", "slug": "founders"}`)
	if code != 201 || out["token"] == nil {
		t.Fatalf("create: %d %v", code, out)
	}
	orgOut := out["organization"].(map[string]any)
	if orgOut["slug"] != "founders" || f.scopeOf(t, out["token"].(string)) != orgOut["id"] {
		t.Errorf("organization: %v", orgOut)
	}
	if roles := orgOut["roles"].([]any); !containsAny(roles, "owner") {
		t.Errorf("the founder's roles: %v", roles)
	}
	// An address that has an account signs in and creates from there.
	if code, out := create(`{` + who + `, "name": "More", "slug": "more"}`); code != 409 {
		t.Errorf("a registered address: %d %v", code, out)
	}
	// A closed realm does not let new people start companies.
	f.svc.Settings.Signup = "closed"
	if code, out := create(`{"email": "late@example.com", "password": "` + testPassword + `", "name": "Late", "slug": "late"}`); code != 403 {
		t.Errorf("closed: %d %v", code, out)
	}
}

// Creating another organization gives a second session, scoped to it, beside the caller's own.
func TestCreatingAnOrganizationOpensASecondSession(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	_, session := f.orgLogin(t, org, "ada@example.com", testPassword, "")
	adaA := session["token"].(string)
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "Second", "slug": "second"}`, jsonHdr(adaA))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	s := out["session"].(map[string]any)
	if s["org"] != out["id"] || f.scopeOf(t, s["token"].(string)) != out["id"] || s["expires_at"] == nil {
		t.Errorf("session: %v", s)
	}
	if f.scopeOf(t, adaA) != org {
		t.Error("the caller's session changed")
	}
	// The same person, now with two sessions.
	if sessions, _ := f.svc.Sessions(context.Background(), mustPrincipal(t, f, adaA)); len(sessions) < 2 {
		t.Errorf("sessions: %d", len(sessions))
	}
}

// Anyone can ask where an address belongs; the answer goes to its mailbox and says nothing to the caller.
func TestWhichCompaniesAmIIn(t *testing.T) {
	f, w, _ := newOrgSignInFixture(t)
	beta := f.orgOf(t, f.ada, "Beta Inc")
	ask := func(email string) int {
		rec, out := f.doH(t, "POST", orgAuthPath+"/find", `{"email": "`+email+`"}`, anon)
		if rec.Code == 202 && out["status"] != "accepted" {
			t.Errorf("answer: %v", out)
		}
		return rec.Code
	}
	for _, email := range []string{"nobody@example.com"} {
		if code := ask(email); code != 202 {
			t.Errorf("%s: %d", email, code)
		}
		if w.RunOnce(context.Background()) {
			t.Errorf("%s: a message was sent to an address with no account", email)
		}
	}
	if code := ask("ada@example.com"); code != 202 {
		t.Fatalf("ada: %d", code)
	}
	in := deliver(t, f, w)
	if in.Kind != "organizations-found" || in.To[0].Email != "ada@example.com" {
		t.Fatalf("message: %+v", in)
	}
	_ = beta
	for _, want := range []string{"Acme Corp", "https://app.acme.example/acme-corp/signin", "Beta Inc", "https://app.acme.example/beta-inc/signin"} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("the message lacks %q:\n%s", want, in.Text)
		}
	}
	// A limit per address.
	var limited bool
	for range 5 {
		if ask("ada@example.com") == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("asking for the same address again and again was never limited")
	}
	// A realm that sets no signin_link has no such route.
	f.svc.Settings.Organizations.SigninLink = ""
	rec, _ := f.doH(t, "POST", orgAuthPath+"/find", `{"email": "ada@example.com"}`, anon)
	if rec.Code != 404 {
		t.Errorf("without signin_link: %d", rec.Code)
	}
}

// The providers of an organization also tell the other ways in, and an unknown one looks like any other.
func TestProvidersOfAnOrganizationListTheMethods(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	var bodies []string
	for _, ref := range []string{org, "acme-corp", "nowhere"} {
		rec, out := f.doH(t, "GET", orgAuthPath+"/"+ref+"/providers", "", nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", ref, rec.Code)
		}
		methods, _ := out["methods"].([]any)
		if len(methods) != 2 || methods[0] != "password" || methods[1] != "magic_link" {
			t.Errorf("%s: methods = %v", ref, methods)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("the answers differ: %q", bodies)
	}
	// The realm's own list is the providers alone.
	if _, out := f.doH(t, "GET", "/v1/acme/_auth/providers", "", nil); out["methods"] != nil {
		t.Errorf("the realm's list has methods: %v", out)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func mustPrincipal(t *testing.T, f *rulesFixture, token string) auth.Principal {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A scoped session says so: in the sign-in answer and in /me, which also says what the person is there.
func TestAScopedSessionSaysWhereItIs(t *testing.T) {
	f, _, org := newOrgSignInFixture(t)
	_, out := f.orgLogin(t, org, "ada@example.com", testPassword, "")
	if out["org"] != org {
		t.Errorf("sign-in answer: %v", out)
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(out["token"].(string)))
	got, _ := me["org"].(map[string]any)
	if got["id"] != org || got["name"] != "Acme Corp" || got["slug"] != "acme-corp" || !containsAny(got["roles"].([]any), "owner") {
		t.Errorf("me.org = %v", got)
	}
	// Everyone's session in a realm with organizations belongs to one: bob's is his own team.
	if _, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(f.bob)); me["org"].(map[string]any)["slug"] != "home-bob" {
		t.Errorf("bob's session: %v", me["org"])
	}
}

// In an invite-only realm, an operator's invitation is what lets a new person start their company.
func TestANewPersonInAnInviteOnlyRealm(t *testing.T) {
	f, _, _ := newOrgSignInFixture(t)
	f.svc.Settings.Signup = "invite"
	create := func(email, invitation string) (int, map[string]any) {
		body := `{"email": "` + email + `", "password": "` + testPassword + `", "name": "Starters", "slug": "starters-` + strings.Split(email, "@")[0] + `"`
		if invitation != "" {
			body += `, "invitation": "` + invitation + `"`
		}
		rec, out := f.doH(t, "POST", orgAuthPath, body+`}`, anon)
		return rec.Code, out
	}
	if code, out := create("nobody@example.com", ""); code != 403 {
		t.Errorf("without an invitation: %d %v", code, out)
	}
	if code, out := create("nobody@example.com", "bdi_nope"); code != 403 {
		t.Errorf("an invitation that is nothing: %d %v", code, out)
	}
	if _, err := f.svc.Find(context.Background(), "nobody@example.com"); err == nil {
		t.Fatal("a refused request made an account")
	}
	_, token, err := f.svc.CreateInvitation(context.Background(), "invited@example.com", 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	if code, out := create("someone-else@example.com", token); code != 403 {
		t.Errorf("an invitation for another address: %d %v", code, out)
	}
	code, out := create("invited@example.com", token)
	if code != 201 || out["org"] == nil || f.scopeOf(t, out["token"].(string)) != out["org"] {
		t.Fatalf("with the invitation: %d %v", code, out)
	}
	if u, _ := f.svc.Find(context.Background(), "invited@example.com"); !u.EmailVerified {
		t.Error("an account made from an invitation to its address starts unverified")
	}
	// It is used up.
	if code, _ := create("invited@example.com", token); code != 403 && code != 409 {
		t.Errorf("the invitation twice: %d", code)
	}
}
