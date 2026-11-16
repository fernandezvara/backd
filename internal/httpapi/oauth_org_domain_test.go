package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// proveDomain stores a claim on domain for the organization in the state given, as a check would have left it.
func (o *orgSignIn) proveDomain(t *testing.T, domain, state string, linkAccounts, discoverable bool) {
	t.Helper()
	now := time.Now().UTC()
	d := auth.OrgDomain{ID: o.org + ":" + domain, Org: o.org, Domain: domain, State: state, Challenge: "backd-verify=x", LinkAccounts: linkAccounts, Discoverable: discoverable, CreatedAt: now}
	if state == auth.DomainVerified {
		d.VerifiedAt = now
	}
	if err := o.svc.Store.PutOrgDomain(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func (o *orgSignIn) identitiesOf(t *testing.T, userID string) []auth.Identity {
	t.Helper()
	ids, err := o.svc.Store.ListIdentities(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A verified domain of the organization lets its connection link an account that has an address on it.
func TestAVerifiedDomainLetsAConnectionLinkAnAccount(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	bob, _ := o.svc.Store.UserByEmail(ctx, "bob@example.com")
	a := o.signInOrg(t, "google", orgGoogle("g-bob", "bob@example.com", "acme.example", true))
	if a.errorCode() != "" {
		t.Fatalf("sign-in: %v", a.result)
	}
	code, out := o.redeem(t, a)
	if code != 200 || out["new_user"] != false || out["user"].(map[string]any)["id"] != bob.ID {
		t.Fatalf("redeem: %d %v", code, out)
	}
	ids := o.identitiesOf(t, bob.ID)
	var linked bool
	for _, i := range ids {
		linked = linked || (i.Provider == "google" && i.Org == o.org && i.Subject == "g-bob" && i.Issuer == "https://accounts.google.com")
	}
	if !linked {
		t.Errorf("no identity of the connection: %+v", ids)
	}
	// The person joined the organization (the account was not a member), and the link is audited.
	if _, err := o.svc.Store.OrgMember(ctx, o.org, bob.ID); err != nil {
		t.Errorf("not a member after the link: %v", err)
	}
	entries, _, _ := o.svc.Store.ListAudit(ctx, auth.AuditFilter{Action: auth.AuditOrgSSOLinked})
	if len(entries) != 1 || entries[0].Details["by"] != "domain" || entries[0].Details["user"] != bob.ID {
		t.Errorf("audit = %+v", entries)
	}
	// The same person again signs in as the same user.
	if again := o.signInOrg(t, "google", orgGoogle("g-bob", "bob@example.com", "acme.example", true)); again.errorCode() != "" {
		t.Errorf("second sign-in: %v", again.result)
	}
}

func TestADomainThatDoesNotQualifyLinksNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		setUp  func(o *orgSignIn)
		person func() map[string]any
	}{
		"no domain proved":       {func(o *orgSignIn) {}, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"a pending claim":        {func(o *orgSignIn) { o.proveDomain(t, "example.com", auth.DomainPending, true, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"a lapsed domain":        {func(o *orgSignIn) { o.proveDomain(t, "example.com", auth.DomainLapsed, true, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"link_accounts off":      {func(o *orgSignIn) { o.proveDomain(t, "example.com", auth.DomainVerified, false, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"another domain":         {func(o *orgSignIn) { o.proveDomain(t, "other.example", auth.DomainVerified, true, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"an unverified account":  {func(o *orgSignIn) { o.proveDomain(t, "example.com", auth.DomainVerified, true, false) }, func() map[string]any { return orgGoogle("g-1", "carl@example.com", "acme.example", true) }},
		"a parent domain proved": {func(o *orgSignIn) { o.proveDomain(t, "com", auth.DomainVerified, true, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
		"the domain is a prefix": {func(o *orgSignIn) { o.proveDomain(t, "ample.com", auth.DomainVerified, true, false) }, func() map[string]any { return orgGoogle("g-1", "bob@example.com", "acme.example", true) }},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrgSignIn(t)
			tc.setUp(o)
			a := o.signInOrg(t, "google", tc.person())
			if a.errorCode() != "account_exists" {
				t.Errorf("sign-in: %v", a.result)
			}
		})
	}
}

// A Microsoft tenant vouches for an address on the domain of its accounts' names, which is what a verified
// domain needs; on another domain it is not an address of the company.
func TestMicrosoftLinksByAVerifiedDomain(t *testing.T) {
	o := newOrgSignIn(t)
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	bob, _ := o.svc.Store.UserByEmail(context.Background(), "bob@example.com")
	a := o.signInOrg(t, "microsoft", orgMicrosoft("oid-bob", "bob@example.com", orgTenant))
	if code, out := o.redeem(t, a); code != 200 || out["new_user"] != false || out["user"].(map[string]any)["id"] != bob.ID {
		t.Fatalf("microsoft: %d %v (%v)", code, out, a.result)
	}
}

func TestEmailDomainsOfAConnectionMustBeProved(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	in := auth.ConnectionInput{ClientID: "org-g-client", HostedDomain: "acme.example", EmailDomains: []string{"example.com"}}
	var bad *auth.OrgInvalidError
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); !errors.As(err, &bad) || bad.Field != "email_domains[0]" {
		t.Fatalf("an unproved domain: %v", err)
	}
	o.proveDomain(t, "example.com", auth.DomainPending, true, false)
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); !errors.As(err, &bad) {
		t.Fatalf("a pending domain: %v", err)
	}
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); err != nil {
		t.Fatalf("a verified domain: %v", err)
	}
	// What a connection had before stays when the proof goes (a realm turning the feature on breaks nothing).
	o.proveDomain(t, "example.com", auth.DomainLapsed, true, false)
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); err != nil {
		t.Errorf("the same list after a lapse: %v", err)
	}
	in.EmailDomains = []string{"example.com", "other.example"}
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", in); !errors.As(err, &bad) || bad.Field != "email_domains[1]" {
		t.Errorf("a new unproved domain beside an old one: %v", err)
	}
}

func TestDiscoveryOnlyLeadsToAUsableConnectionOfAProvedDiscoverableDomain(t *testing.T) {
	o := newOrgSignIn(t)
	ctx := context.Background()
	discover := func(email string) []any {
		t.Helper()
		_, out := o.do(t, "POST", "/v1/acme/_auth/orgs/discover", `{"email": "`+email+`"}`)
		items, _ := out["items"].([]any)
		return items
	}
	if got := discover("x@example.com"); len(got) != 0 {
		t.Fatalf("no domain: %v", got)
	}
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	if got := discover("x@example.com"); len(got) != 0 {
		t.Errorf("not discoverable: %v", got)
	}
	o.proveDomain(t, "example.com", auth.DomainVerified, true, true)
	got := discover("X@Example.COM")
	if len(got) != 1 || got[0].(map[string]any)["org"] != o.org || got[0].(map[string]any)["name"] != "Acme Corp" || len(got[0].(map[string]any)["providers"].([]any)) != 2 {
		t.Fatalf("discoverable: %v", got)
	}
	// Only the providers that work: a disabled connection is not offered.
	off := false
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "microsoft", auth.ConnectionInput{ClientID: orgMSApp, Tenant: orgTenant, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := discover("x@example.com"); len(got) != 1 || len(got[0].(map[string]any)["providers"].([]any)) != 1 {
		t.Errorf("one provider off: %v", got)
	}
	if _, err := o.svc.AdminSetOrgConnection(ctx, o.org, "google", auth.ConnectionInput{ClientID: "org-g-client", HostedDomain: "acme.example", Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := discover("x@example.com"); len(got) != 0 {
		t.Errorf("no working connection: %v", got)
	}
	// An address that is not one, and a lapsed domain, answer the same.
	for _, e := range []string{"not an address", "", "x@sub.example.com"} {
		if got := discover(e); len(got) != 0 {
			t.Errorf("%q: %v", e, got)
		}
	}
	o.proveDomain(t, "example.com", auth.DomainLapsed, true, true)
	if got := discover("x@example.com"); len(got) != 0 {
		t.Errorf("lapsed: %v", got)
	}
}
