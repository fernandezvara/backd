package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

var requestIDs = regexp.MustCompile(`"request_id":"[^"]*"`)

const (
	orgTenant = "11111111-2222-3333-4444-555555555555"
	orgMSApp  = "22222222-3333-4444-5555-666666666666"
)

// orgSignIn is an oauth fixture with an organization that has a Google and a Microsoft
// connection, both played by the fake provider.
type orgSignIn struct {
	*oauthFixture
	org  string
	base string // /v1/acme/_auth/orgs/{org}/oauth
}

func newOrgSignIn(t *testing.T) *orgSignIn {
	t.Helper()
	f := newOAuthFixture(t)
	ctx := context.Background()
	made, err := f.svc.AdminCreateOrg(ctx, "Acme Corp", "acme-corp", f.adaID)
	if err != nil {
		t.Fatal(err)
	}
	o := &orgSignIn{oauthFixture: f, org: made.Org.ID, base: "/v1/acme/_auth/orgs/" + made.Org.ID + "/oauth"}
	if _, err := f.svc.AdminSetOrgConnection(ctx, o.org, "google", auth.ConnectionInput{ClientID: "org-g-client", ClientSecret: "org-g-secret", HostedDomain: "acme.example", NativeClientIDs: []string{"org-g-native"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdminSetOrgConnection(ctx, o.org, "microsoft", auth.ConnectionInput{ClientID: orgMSApp, ClientSecret: "org-m-secret", Tenant: orgTenant}); err != nil {
		t.Fatal(err)
	}
	return o
}

func orgGoogle(sub, email, hd string, verified bool) map[string]any {
	c := map[string]any{"iss": "https://accounts.google.com", "aud": "org-g-client", "sub": sub, "email": email, "email_verified": verified}
	if hd != "" {
		c["hd"] = hd
	}
	return c
}

func orgMicrosoft(oid, email, tenant string) map[string]any {
	return map[string]any{"iss": "https://login.microsoftonline.com/" + tenant + "/v2.0", "aud": orgMSApp, "tid": tenant, "oid": oid, "sub": "s-" + oid, "email": email}
}

// start begins a sign-in at base and returns the provider's authorize URL (or the answer).
func (o *orgSignIn) start(t *testing.T, base, provider string) (verifier string, code int, body string, location string) {
	t.Helper()
	verifier, challenge, _ := auth.NewPKCE()
	q := url.Values{"redirect_to": {appRedirect}, "code_challenge": {challenge}}
	rec, _ := o.doH(t, "GET", base+"/"+provider+"/start?"+q.Encode(), "", nil)
	return verifier, rec.Code, rec.Body.String(), rec.Header().Get("Location")
}

// signInOrg runs the redirect flow of the organization's connection.
func (o *orgSignIn) signInOrg(t *testing.T, provider string, person map[string]any) attempt {
	t.Helper()
	o.idp.SetUser(person)
	verifier, code, body, loc := o.start(t, o.base, provider)
	if code != http.StatusFound {
		t.Fatalf("start: %d %s", code, body)
	}
	return o.finish(t, provider, loc, verifier, false)
}

func TestOrganizationSignInWithGoogle(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	a := o.signInOrg(t, "google", orgGoogle("g-org-1", "ana@acme.example", "acme.example", true))
	if a.errorCode() != "" {
		t.Fatalf("sign-in: %v", a.result)
	}
	// The exchange used the organization's application and secret.
	reqs := o.idp.TokenRequests()
	if last := reqs[len(reqs)-1]; last.Get("client_id") != "org-g-client" || last.Get("client_secret") != "org-g-secret" ||
		last.Get("redirect_uri") != "https://api.acme.example/v1/acme/_auth/orgs/"+o.org+"/oauth/google/callback" {
		t.Errorf("token request = %v", last)
	}
	code, out := o.redeem(t, a)
	if code != 200 || out["new_user"] != true {
		t.Fatalf("redeem: %d %v", code, out)
	}
	userID := out["user"].(map[string]any)["id"].(string)
	// The identity is the organization's, not the realm's.
	id, err := o.svc.Store.IdentityIn(ctx, "google", o.org, "g-org-1")
	if err != nil || id.UserID != userID || id.Org != o.org {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if _, err := o.svc.Store.Identity(ctx, "google", "g-org-1"); err == nil {
		t.Error("the identity is also a realm one")
	}
	// Again: the same user, with a slug in the address too.
	o.idp.SetUser(orgGoogle("g-org-1", "ana@acme.example", "acme.example", true))
	verifier, code2, _, loc := o.start(t, "/v1/acme/_auth/orgs/acme-corp/oauth", "google")
	if code2 != http.StatusFound {
		t.Fatalf("start by slug: %d", code2)
	}
	b := o.finish(t, "google", loc, verifier, false)
	if _, out := o.redeem(t, b); out["user"].(map[string]any)["id"] != userID || out["new_user"] == true {
		t.Errorf("second sign-in: %v", out)
	}
	// An address the provider vouches for isn't marked verified for a connection's people.
	u, _ := o.svc.Store.UserByID(ctx, userID)
	if u.EmailVerified {
		t.Errorf("an organization's person was marked verified: %+v", u)
	}
}

func TestOrganizationSignInChecksWhoComesIn(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	for name, person := range map[string]map[string]any{
		"another workspace":    orgGoogle("g-1", "x@other.example", "other.example", true),
		"a personal account":   orgGoogle("g-2", "x@gmail.example", "", true),
		"an unverified email":  orgGoogle("g-3", "x@acme.example", "acme.example", false),
		"the domain in suffix": orgGoogle("g-4", "x@acme.example", "evil-acme.example", true),
	} {
		if a := o.signInOrg(t, "google", person); a.errorCode() != "not_allowed" || a.result.Query().Get("backd_code") != "" {
			t.Errorf("%s: %v", name, a.result)
		}
	}
	if u, err := o.svc.Store.UserByEmail(ctx, "x@acme.example"); err == nil {
		t.Errorf("a refused sign-in created a user: %+v", u)
	}
	// email_domains narrows further.
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", auth.ConnectionInput{ClientID: "org-g-client", HostedDomain: "acme.example", EmailDomains: []string{"staff.acme.example"}}); err != nil {
		t.Fatal(err)
	}
	if a := o.signInOrg(t, "google", orgGoogle("g-5", "ana@acme.example", "acme.example", true)); a.errorCode() != "not_allowed" {
		t.Errorf("an email outside email_domains: %v", a.result)
	}
	if a := o.signInOrg(t, "google", orgGoogle("g-6", "ana@staff.acme.example", "acme.example", true)); a.errorCode() != "" {
		t.Errorf("an email inside email_domains: %v", a.result)
	}
}

func TestOrganizationSignInWithMicrosoft(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	a := o.signInOrg(t, "microsoft", orgMicrosoft("oid-1", "ben@acme.example", orgTenant))
	if a.errorCode() != "" {
		t.Fatalf("sign-in: %v", a.result)
	}
	if code, out := o.redeem(t, a); code != 200 || out["new_user"] != true {
		t.Fatalf("redeem: %d %v", code, out)
	}
	if _, err := o.svc.Store.IdentityIn(ctx, "microsoft", o.org, orgTenant+":oid-1"); err != nil {
		t.Errorf("identity: %v", err)
	}
	// Another tenant's person is not the company's: the token's issuer is not its tenant.
	if a := o.signInOrg(t, "microsoft", orgMicrosoft("oid-2", "eve@evil.example", "99999999-aaaa-bbbb-cccc-dddddddddddd")); a.errorCode() == "" {
		t.Errorf("another tenant came in: %v", a.result)
	}
	// An address outside email_domains.
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "microsoft", auth.ConnectionInput{ClientID: orgMSApp, Tenant: orgTenant, EmailDomains: []string{"staff.acme.example"}}); err != nil {
		t.Fatal(err)
	}
	if a := o.signInOrg(t, "microsoft", orgMicrosoft("oid-3", "ben@acme.example", orgTenant)); a.errorCode() != "not_allowed" {
		t.Errorf("outside email_domains: %v", a.result)
	}
}

// An organization sign-in never links to an account by address.
func TestOrganizationSignInNeverLinksByEmail(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	// ada has a verified account with this address, and the connection's Google says it is verified.
	if _, err := o.svc.Store.UserByEmail(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	a := o.signInOrg(t, "google", orgGoogle("g-ada", "ada@example.com", "acme.example", true))
	if a.errorCode() != "account_exists" {
		t.Fatalf("an existing address: %v", a.result)
	}
	if ids, _ := o.svc.Store.ListIdentities(ctx, o.adaID); len(ids) != 1 {
		t.Errorf("an identity was added to the account: %+v", ids)
	}
	// The same realm sign-in through Google would have linked (the control).
	if a := o.signIn(t, "google", googleUser("g-ada-realm", "ada@example.com", true), nil); a.errorCode() != "" {
		t.Errorf("realm google: %v", a.result)
	}
}

// What the callback of one place can't finish for another.
func TestOrganizationStateStaysWhereItBegan(t *testing.T) {
	o := newOrgSignIn(t)
	other, err := o.svc.AdminCreateOrg(context.Background(), "Other Corp", "", o.adaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.svc.AdminSetOrgConnection(context.Background(), other.Org.ID, "google", auth.ConnectionInput{ClientID: "org-g-client", ClientSecret: "other-secret", HostedDomain: "acme.example"}); err != nil {
		t.Fatal(err)
	}
	person := orgGoogle("g-mix", "mix@acme.example", "acme.example", true)

	try := func(name, startBase, callbackBase string) {
		t.Helper()
		o.idp.SetUser(person)
		verifier, code, body, loc := o.start(t, startBase, "google")
		if code != http.StatusFound {
			t.Fatalf("%s: start %d %s", name, code, body)
		}
		back, _ := url.Parse(o.idp.Authorize(loc))
		rec, _ := o.doH(t, "GET", callbackBase+"/google/callback?"+back.RawQuery, "", nil)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s: a callback of another place finished the sign-in: %d %s", name, rec.Code, rec.Header().Get("Location"))
		}
		_ = verifier
	}
	try("org A finished at org B", o.base, "/v1/acme/_auth/orgs/"+other.Org.ID+"/oauth")
	try("org finished at the realm", o.base, oauthBase)
	try("realm finished at the org", oauthBase, o.base)
	if u, err := o.svc.Store.UserByEmail(context.Background(), "mix@acme.example"); err == nil {
		t.Errorf("a user was made: %+v", u)
	}
}

// Every reason a connection can't be used answers alike, and the list agrees with start.
func TestOrganizationProvidersAnswerAlikeWhenUnavailable(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	listOf := func(base string) (int, []string, string) {
		rec, out := o.doH(t, "GET", base+"/providers", "", nil)
		var names []string
		for _, v := range out["items"].([]any) {
			names = append(names, v.(string))
		}
		return rec.Code, names, rec.Header().Get("Cache-Control")
	}
	orgAuth := "/v1/acme/_auth/orgs/"
	if code, names, cache := listOf(orgAuth + o.org); code != 200 || strings.Join(names, ",") != "google,microsoft" || cache != "public, max-age=60" {
		t.Fatalf("list = %d %v %q", code, names, cache)
	}
	// The realm's own list: the providers realm.yaml declares.
	if code, names, _ := listOf("/v1/acme/_auth"); code != 200 || strings.Join(names, ",") != "apple,google,microsoft" {
		t.Errorf("realm list = %d %v", code, names)
	}

	startAnswer := func() (int, string) {
		_, code, body, _ := o.start(t, o.base, "google")
		return code, body
	}
	setConn := func(in auth.ConnectionInput) {
		if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); err != nil {
			t.Fatal(err)
		}
	}
	disabled := false
	var reference string
	check := func(name string, code int, body string) {
		t.Helper()
		if code != http.StatusNotFound {
			t.Errorf("%s: start answered %d", name, code)
		}
		body = requestIDs.ReplaceAllString(body, `"request_id":""`)
		if reference == "" {
			reference = body
		}
		if body != reference {
			t.Errorf("%s: the answer differs from the others:\n%s\n%s", name, body, reference)
		}
	}
	// A provider the organization never connected.
	_, code, body, _ := o.start(t, o.base, "apple")
	check("never connected", code, body)
	// An organization that isn't there, and one the address doesn't match.
	_, code, body, _ = o.start(t, orgAuth+"nope/oauth", "google")
	check("unknown organization", code, body)
	// Disabled.
	setConn(auth.ConnectionInput{ClientID: "org-g-client", HostedDomain: "acme.example", Enabled: &disabled})
	code, body = startAnswer()
	check("disabled", code, body)
	if _, names, _ := listOf(orgAuth + o.org); strings.Join(names, ",") != "microsoft" {
		t.Errorf("list with google disabled = %v", names)
	}
	enabled := true
	setConn(auth.ConnectionInput{ClientID: "org-g-client", HostedDomain: "acme.example", Enabled: &enabled})
	// Without its secret.
	if err := o.svc.DeleteOrgSecret(ctx, o.org, "SSO_GOOGLE_CLIENT_SECRET"); err != nil {
		t.Fatal(err)
	}
	code, body = startAnswer()
	check("no secret", code, body)
	if _, names, _ := listOf(orgAuth + o.org); strings.Join(names, ",") != "microsoft" {
		t.Errorf("list without the secret = %v", names)
	}
	if err := o.svc.SetOrgSecret(ctx, o.org, "SSO_GOOGLE_CLIENT_SECRET", "org-g-secret", "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ = startAnswer(); code != http.StatusFound {
		t.Fatalf("restored: %d", code)
	}
	// Organization orphaned or deleted.
	for _, status := range []string{auth.OrgOrphaned, auth.OrgDeleted} {
		if _, err := o.svc.Store.UpdateOrg(ctx, o.org, auth.OrgUpdate{Status: status}, time.Now()); err != nil {
			t.Fatal(err)
		}
		code, body = startAnswer()
		check(status, code, body)
		if _, names, _ := listOf(orgAuth + o.org); len(names) != 0 {
			t.Errorf("list of a %s organization = %v", status, names)
		}
	}
	// The unknown organization, one without connections and one with the switch off all list nothing.
	if code, names, _ := listOf(orgAuth + "nope"); code != 200 || len(names) != 0 {
		t.Errorf("unknown organization lists %d %v", code, names)
	}
	o.svc.Settings.Organizations.SSO = nil
	code, body = startAnswer()
	check("switch off", code, body)
	if code, names, _ := listOf(orgAuth + o.org); code != 200 || len(names) != 0 {
		t.Errorf("switch off lists %d %v", code, names)
	}
}

func TestOrganizationNativeSignIn(t *testing.T) {
	o := newOrgSignIn(t)
	idToken := func(claims map[string]any, nonce string) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id_token": o.idp.IDToken(t, claims), "nonce": nonce})
		rec, out := o.doH(t, "POST", o.base+"/google/id-token", string(body), map[string]string{"Content-Type": "application/json"})
		return rec.Code, out
	}
	native := func(sub, email, hd string, verified bool) map[string]any {
		c := orgGoogle(sub, email, hd, verified)
		c["aud"], c["nonce"] = "org-g-native", "n-1"
		return c
	}
	if code, out := idToken(native("g-n1", "nat@acme.example", "acme.example", true), "n-1"); code != 200 || out["token"] == nil {
		t.Fatalf("native: %d %v", code, out)
	}
	// The web client's audience is accepted too; the realm's native client is not.
	web := orgGoogle("g-n1", "nat@acme.example", "acme.example", true)
	web["nonce"] = "n-1"
	if code, _ := idToken(web, "n-1"); code != 200 {
		t.Errorf("web audience: %d", code)
	}
	realmNative := native("g-n1", "nat@acme.example", "acme.example", true)
	realmNative["aud"] = "g-native"
	if code, out := idToken(realmNative, "n-1"); code != 401 {
		t.Errorf("the realm's native client at an organization: %d %v", code, out)
	}
	for name, c := range map[string]map[string]any{
		"another workspace": native("g-n2", "x@other.example", "other.example", true),
		"personal":          native("g-n3", "x@gmail.example", "", true),
		"unverified":        native("g-n4", "x@acme.example", "acme.example", false),
	} {
		if code, out := idToken(c, "n-1"); code != 403 || errCode(out) != "not_allowed" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if u, err := o.svc.Store.UserByEmail(context.Background(), "x@acme.example"); err == nil {
		t.Errorf("a refused native sign-in made a user: %+v", u)
	}
}

// A person holds one identity per provider per organization, and may hold the realm's too.
func TestIdentitiesAreKeyedByOrganization(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	put := func(id, user, org, subject string) error {
		return f.svc.Store.PutIdentity(ctx, auth.Identity{ID: id, UserID: user, Provider: "google", Org: org, Subject: subject, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	}
	if err := put("i1", f.adaID, "", "sub-1"); err != nil {
		t.Fatal(err)
	}
	// The same person at an organization, another at a second one, the same subject for two people.
	for id, args := range map[string][3]string{"i2": {f.adaID, "orgA", "sub-1"}, "i3": {f.adaID, "orgB", "sub-1"}, "i4": {f.bobID, "orgA", "sub-2"}} {
		if err := put(id, args[0], args[1], args[2]); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	// Not twice for one organization, and not one subject for two people.
	if err := put("i5", f.adaID, "orgA", "sub-9"); err == nil {
		t.Error("a second google identity for one user in one organization")
	}
	if err := put("i6", f.bobID, "orgA", "sub-1"); err == nil {
		t.Error("one subject belongs to two users in an organization")
	}
	if id, err := f.svc.Store.IdentityOfIn(ctx, f.adaID, "google", "orgB"); err != nil || id.Subject != "sub-1" {
		t.Errorf("IdentityOfIn = %+v, %v", id, err)
	}
	// Unlinking takes the organization.
	if err := f.svc.UnlinkIn(ctx, f.adaID, "google", "orgA", "self"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Store.IdentityOfIn(ctx, f.adaID, "google", "orgA"); err == nil {
		t.Error("the identity is still there")
	}
	if _, err := f.svc.Store.IdentityOfIn(ctx, f.adaID, "google", "orgB"); err != nil {
		t.Errorf("another organization's identity went too: %v", err)
	}
	if _, err := f.svc.Store.IdentityOf(ctx, f.adaID, "google"); err != nil {
		t.Errorf("the realm's identity went too: %v", err)
	}
	if err := f.svc.UnlinkIn(ctx, f.adaID, "google", "orgA", "self"); err == nil {
		t.Error("unlinked twice")
	}
}
