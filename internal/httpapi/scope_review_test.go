package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// Review of scoped sessions (design/scoped-sessions.md §14): paths a sign-in could take around
// what an organization requires.

// An attempt with the realm's own provider that began before the organization required its
// connection can't finish after.
func TestAnAttemptStartedBeforeRequiringTheConnectionCannotFinishAfter(t *testing.T) {
	f := newOrgOAuthFixture(t)
	ctx := context.Background()
	made, err := f.svc.AdminCreateOrg(ctx, "Plain Inc", "plain-inc", f.adaID)
	if err != nil {
		t.Fatal(err)
	}
	// Microsoft is the organization's connection; Google stays the realm's own provider there.
	if _, err := f.svc.AdminSetOrgConnection(ctx, made.Org.ID, "microsoft", auth.ConnectionInput{ClientID: orgMSApp, ClientSecret: "org-m-secret", Tenant: orgTenant}); err != nil {
		t.Fatal(err)
	}
	f.idp.SetUser(googleUser("g-ada-plain", "ada@example.com", true))
	verifier, challenge, _ := auth.NewPKCE()
	q := url.Values{"redirect_to": {appRedirect}, "code_challenge": {challenge}}
	rec, _ := f.doH(t, "GET", "/v1/acme/_auth/orgs/"+made.Org.ID+"/oauth/google/start?"+q.Encode(), "", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	on := true
	if _, err := f.svc.Store.UpdateOrg(ctx, made.Org.ID, auth.OrgUpdate{SSORequired: &on}, f.svc.Clock()); err != nil {
		t.Fatal(err)
	}
	a := f.finish(t, "google", rec.Header().Get("Location"), verifier, false)
	if a.errorCode() != "sso_required" || a.result.Query().Get("backd_code") != "" {
		t.Errorf("finished at an organization that requires its connection: %v", a.result)
	}
}

// A login code issued before the requirement can't be redeemed after it: the session is made in one
// place, and that place asks.
func TestACodeIssuedBeforeRequiringTheConnectionCannotBeRedeemedAfter(t *testing.T) {
	s := newSSOOrg(t)
	ctx := context.Background()
	verifier, challenge, _ := auth.NewPKCE()
	// A code for the organization from a way that is not its connection (a link, say).
	code, err := s.svc.IssueLoginCodeIn(ctx, s.anaID, auth.MagicLinkLoginMethod, challenge, false, nil, s.org)
	if err != nil {
		t.Fatal(err)
	}
	if rec, out := s.fixture.doH(t, "PATCH", orgsPath+"/"+s.org, `{"sso_required": true}`, s.withProof(t, s.anaTok)); rec.Code != 200 {
		t.Fatalf("turn it on: %d %v", rec.Code, out)
	}
	rec, out := s.fixture.doH(t, "POST", "/v1/acme/_auth/token", `{"code": "`+code+`", "code_verifier": "`+verifier+`"}`, anon)
	if rec.Code != 400 || errCode(out) != "sso_required" {
		t.Errorf("a code from before: %d %v", rec.Code, out)
	}
	// The connection's own still works.
	_, out = s.redeem(t, s.signInOrg(t, "google", orgGoogle("g-ana", "ana@acme.example", "acme.example", true)))
	if out["token"] == nil {
		t.Errorf("the connection after: %v", out)
	}
}

// Who has no identity on the connection is for the managers who can turn the requirement on.
func TestOnlyManagersListWhoHasNoIdentityOnTheConnection(t *testing.T) {
	s := newSSOOrg(t)
	// A plain member (no manage capability).
	_, out := s.redeem(t, s.signInOrg(t, "microsoft", orgMicrosoft("oid-m", "mia@acme.example", orgTenant)))
	mia := out["token"].(string)
	if rec, _ := s.fixture.doH(t, "GET", orgsPath+"/"+s.org+"/members?sso=false", "", bearer(mia)); rec.Code != 403 {
		t.Errorf("a plain member: %d", rec.Code)
	}
	if rec, _ := s.fixture.doH(t, "GET", orgsPath+"/"+s.org+"/members?sso=false", "", bearer(s.anaTok)); rec.Code != 200 {
		t.Errorf("a manager: %d", rec.Code)
	}
}

// Each session says the organization it belongs to and, when a connection opened it, which.
func TestSessionsSayWhereTheyBelong(t *testing.T) {
	s := newSSOOrg(t)
	rec, out := s.fixture.doH(t, "GET", "/v1/acme/_auth/sessions", "", bearer(s.anaTok))
	if rec.Code != 200 {
		t.Fatalf("sessions: %d %v", rec.Code, out)
	}
	items := out["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no sessions")
	}
	cur := items[0].(map[string]any)
	for _, it := range items {
		if m := it.(map[string]any); m["current"] == true {
			cur = m
		}
	}
	if cur["org"] != s.org || cur["connection"] != "google" {
		t.Errorf("the connection's session = %v", cur)
	}
	rec, out = s.fixture.doH(t, "GET", "/v1/acme/_auth/sessions", "", bearer(s.adaTok))
	if rec.Code != 200 {
		t.Fatalf("ada's sessions: %d %v", rec.Code, out)
	}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["current"] == true && (m["org"] != s.org || m["connection"] != nil) {
			t.Errorf("a password session = %v", m)
		}
	}
}

// A slug shows that it exists in one place, a taken slug at creation: that place is limited per
// address, and so are the sign-in calls that name an organization.
func TestSlugProbingIsLimited(t *testing.T) {
	f, _, _ := newOrgSignInFixture(t)
	body := `{"email": "probe@example.com", "password": "dev-p4ssw0rd!", "name": "Probe", "slug": "acme-corp"}`
	var last int
	for range orgCreationRate + 1 {
		rec, _ := f.doH(t, "POST", orgAuthPath, body, anon)
		last = rec.Code
	}
	if last != 429 {
		t.Errorf("the %dth try to start a company: %d", orgCreationRate+1, last)
	}
	for range orgSignInGateRate + 1 {
		rec, _ := f.doH(t, "POST", orgAuthPath+"/walking-slugs/login", `{"email": "x@example.com", "password": "x"}`, anon)
		last = rec.Code
	}
	if last != 429 {
		t.Errorf("the %dth sign-in call: %d", orgSignInGateRate+1, last)
	}
}
