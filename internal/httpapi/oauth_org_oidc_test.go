package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// newOrgOIDC is the organization sign-in fixture with an OpenID Connect connection whose issuer is the
// fake provider, for an organization that has proved example.com.
func newOrgOIDC(t *testing.T) *orgSignIn {
	t.Helper()
	o := newOrgSignIn(t)
	sso := o.svc.Settings.Organizations.SSO
	sso.Providers = append(sso.Providers, "oidc")
	sso.MaxPerOrg = 3
	o.svc.Dev = true // the fake provider is a local http server
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	in := auth.ConnectionInput{ClientID: "org-o-client", ClientSecret: "org-o-secret", Issuer: o.idp.URL, JoinRoles: []string{"editor"}}
	if _, err := o.svc.AdminSetOrgConnection(context.Background(), o.org, "oidc", in); err != nil {
		t.Fatal(err)
	}
	return o
}

func orgOIDC(o *orgSignIn, sub, email string, verified bool) map[string]any {
	return map[string]any{"iss": o.idp.URL, "aud": "org-o-client", "sub": sub, "email": email, "email_verified": verified}
}

func TestAnOIDCConnectionAdmitsPeopleOfAProvedDomain(t *testing.T) {
	o := newOrgOIDC(t)
	ctx := context.Background()
	a := o.signInOrg(t, "oidc", orgOIDC(o, "kc-1", "zoe@example.com", true))
	if a.errorCode() != "" {
		t.Fatalf("sign-in: %v", a.result)
	}
	code, out := o.redeem(t, a)
	user := out["user"].(map[string]any)
	if code != 200 || out["new_user"] != true || user["email"] != "zoe@example.com" || user["email_verified"] != false {
		t.Fatalf("redeem: %d %v", code, out)
	}
	// The identity is the organization's, bound to the issuer, and the person joined with the join roles.
	id, err := o.svc.Store.IdentityIn(ctx, "oidc", o.org, o.idp.URL, "kc-1")
	if err != nil || id.UserID != user["id"] {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if m, err := o.svc.Store.OrgMember(ctx, o.org, id.UserID); err != nil || len(m.Roles) != 1 || m.Roles[0].Role != "editor" {
		t.Errorf("membership = %+v, %v", m, err)
	}
	// The same person again.
	if again := o.signInOrg(t, "oidc", orgOIDC(o, "kc-1", "zoe@example.com", true)); again.errorCode() != "" {
		t.Errorf("second sign-in: %v", again.result)
	}
	// An address on the domain of an account that is verified links it, as with the other providers.
	bob, _ := o.svc.Store.UserByEmail(ctx, "bob@example.com")
	linked := o.signInOrg(t, "oidc", orgOIDC(o, "kc-bob", "bob@example.com", true))
	if _, out := o.redeem(t, linked); out["user"].(map[string]any)["id"] != bob.ID || out["new_user"] != false {
		t.Errorf("link: %v", out)
	}
}

func TestAnOIDCConnectionRefusesWhoIsNotOfAProvedDomain(t *testing.T) {
	o := newOrgOIDC(t)
	ctx := context.Background()
	for name, person := range map[string]map[string]any{
		"an address on another domain": orgOIDC(o, "kc-2", "mallory@evil.example", true),
		"a subdomain not proved":       orgOIDC(o, "kc-3", "mallory@eng.example.com", true),
		"an address not verified":      orgOIDC(o, "kc-4", "mallory@example.com", false),
		"no address":                   {"iss": o.idp.URL, "aud": "org-o-client", "sub": "kc-5"},
	} {
		if a := o.signInOrg(t, "oidc", person); a.errorCode() != "not_allowed" {
			t.Errorf("%s: %v", name, a.result)
		}
	}
	if u, err := o.svc.Store.UserByEmail(ctx, "mallory@evil.example"); err == nil {
		t.Errorf("a refused person was made a user: %+v", u)
	}
	// What backs it is looked at each time: the domain lapses and the connection is gone.
	o.proveDomain(t, "example.com", auth.DomainLapsed, true, false)
	_, code, _, _ := o.start(t, o.base, "oidc")
	if code != http.StatusNotFound {
		t.Errorf("a connection whose domain lapsed: %d", code)
	}
	o.proveDomain(t, "example.com", auth.DomainVerified, true, false)
	if _, code, _, _ := o.start(t, o.base, "oidc"); code != http.StatusFound {
		t.Errorf("after the domain came back: %d", code)
	}
	// So does the realm's list of issuers.
	o.svc.Dev = false
	if _, code, _, _ := o.start(t, o.base, "oidc"); code != http.StatusNotFound {
		t.Errorf("an issuer the realm does not list: %d", code)
	}
}

func TestAnOIDCConnectionNeedsAnAllowedIssuerAndAVerifiedDomain(t *testing.T) {
	o := newOrgOIDC(t)
	ctx := context.Background()
	set := func(in auth.ConnectionInput) error {
		in.ClientID = "org-o-client"
		_, err := o.svc.AdminSetOrgConnection(ctx, o.org, "oidc", in)
		return err
	}
	var bad *auth.OrgInvalidError
	// An issuer the realm doesn't list, with the dev allowance off: nothing outside the patterns.
	o.svc.Dev = false
	o.svc.Settings.Organizations.SSO.OIDCIssuers = nil
	for _, issuer := range []string{"https://evil.example", "https://169.254.169.254", "http://localhost:8080", o.idp.URL, ""} {
		if err := set(auth.ConnectionInput{Issuer: issuer}); !errors.As(err, &bad) || bad.Field != "issuer" {
			t.Errorf("issuer %q: %v", issuer, err)
		}
	}
	p, msg := registry.ParseIssuerPattern("https://*.okta.com")
	if msg != "" {
		t.Fatal(msg)
	}
	o.svc.Settings.Organizations.SSO.OIDCIssuers = []registry.IssuerPattern{p}
	if err := set(auth.ConnectionInput{Issuer: "https://acme.okta.com/oauth2/default"}); err != nil {
		t.Errorf("an issuer of the list: %v", err)
	}
	if err := set(auth.ConnectionInput{Issuer: "https://acme.okta.com.evil.example"}); !errors.As(err, &bad) {
		t.Errorf("a look-alike: %v", err)
	}
	// Tenant and Workspace settings don't belong to it, and domains must be the organization's proved ones.
	o.svc.Dev = true
	if err := set(auth.ConnectionInput{Issuer: o.idp.URL, Tenant: orgTenant}); !errors.As(err, &bad) {
		t.Errorf("a tenant: %v", err)
	}
	if err := set(auth.ConnectionInput{Issuer: o.idp.URL, Domains: []string{"other.example"}}); !errors.As(err, &bad) || bad.Field != "domains[0]" {
		t.Errorf("a domain not proved: %v", err)
	}
	if err := set(auth.ConnectionInput{Issuer: o.idp.URL, Domains: []string{"example.com"}}); err != nil {
		t.Errorf("a domain proved: %v", err)
	}
	// Without a verified domain there is no connection to make.
	o.proveDomain(t, "example.com", auth.DomainLapsed, true, false)
	if err := set(auth.ConnectionInput{Issuer: o.idp.URL}); !errors.As(err, &bad) || bad.Field != "domains" {
		t.Errorf("no verified domain: %v", err)
	}
}

func TestChangingTheIssuerLeavesTheOldIdentitiesDormant(t *testing.T) {
	o := newOrgOIDC(t)
	ctx := context.Background()
	if a := o.signInOrg(t, "oidc", orgOIDC(o, "kc-1", "zoe@example.com", true)); a.errorCode() != "" {
		t.Fatalf("sign-in: %v", a.result)
	}
	other := "http://localhost:1/realms/other"
	view, err := o.svc.AdminSetOrgConnection(ctx, o.org, "oidc", auth.ConnectionInput{ClientID: "org-o-client", Issuer: other})
	if err != nil || view.DormantIdentities != 1 {
		t.Fatalf("the change: %+v, %v", view, err)
	}
	zoe, _ := o.svc.Store.UserByEmail(ctx, "zoe@example.com")
	ids, _ := o.svc.Identities(ctx, zoe.ID)
	if len(ids) == 0 || !o.svc.IdentityDormant(ctx, ids[len(ids)-1]) {
		t.Errorf("identities = %+v", ids)
	}
	// Setting it to the same issuer again says nothing about dormant identities.
	if view, _ = o.svc.AdminSetOrgConnection(ctx, o.org, "oidc", auth.ConnectionInput{ClientID: "org-o-client", Issuer: other}); view.DormantIdentities != 0 {
		t.Errorf("no change: %+v", view)
	}
}

func TestAnOIDCConnectionHasNoNativeSignIn(t *testing.T) {
	o := newOrgOIDC(t)
	rec, _ := o.doH(t, "POST", o.base+"/oidc/id-token", `{"id_token":"x","nonce":"n"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("id-token: %d", rec.Code)
	}
	// And the list of providers of the organization names it.
	_, out := o.do(t, "GET", "/v1/acme/_auth/orgs/"+o.org+"/providers", "")
	if !containsItem(out["items"], "oidc") {
		t.Errorf("providers = %v", out)
	}
}
