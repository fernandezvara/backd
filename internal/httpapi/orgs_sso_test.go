package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

const (
	ssoTenant = "11111111-2222-3333-4444-555555555555"
	ssoGoogle = `{"client_id": "123-abc.apps.googleusercontent.com", "client_secret": "g-client-secret", "hosted_domain": "acme.example", "join_roles": ["editor"]}`
)

// A manager sets up the organization's connection; the secret goes in and never comes out.
func TestOrganizationConnectionsAsAManager(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	org := f.orgOf(t, f.ada, "Acme Corp")
	url := orgsPath + "/" + org + "/sso"

	// No secrets key on the instance: the secret can't be kept, so nothing is saved.
	if rec, out := f.doH(t, "PUT", url+"/google", ssoGoogle, f.withProof(t, f.ada)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a secrets key: %d %v", rec.Code, out)
	}
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}

	// Who: a session, a member, a holder of manage: sso.
	if rec, _ := f.doH(t, "GET", url, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", url, "", bearer(f.bob)); rec.Code != http.StatusForbidden || errCode(out) != "not_org_member" {
		t.Errorf("a stranger: %d %v", rec.Code, out)
	}
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"admin"}); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", url, "", bearer(f.bob)); rec.Code != http.StatusForbidden || errCode(out) != "forbidden" {
		t.Errorf("an admin without manage: sso: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PUT", url+"/google", ssoGoogle, f.withProof(t, f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("an admin sets: %d %v", rec.Code, out)
	}

	// A change needs a fresh proof; the question of who may comes first.
	if rec, out := f.doH(t, "PUT", url+"/google", ssoGoogle, jsonHdr(f.ada)); rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}
	rec, out := f.doH(t, "PUT", url+"/google", ssoGoogle, f.withProof(t, f.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d %v", rec.Code, out)
	}
	if strings.Contains(rec.Body.String(), "g-client-secret") {
		t.Fatalf("the client secret is in the answer: %s", rec.Body)
	}
	want := "https://api.acme.example/v1/acme/_auth/orgs/" + org + "/oauth/google/callback"
	if out["redirect_uri"] != want || out["client_secret_set"] != true || out["hosted_domain"] != "acme.example" || out["enabled"] != true || out["provider"] != "google" {
		t.Errorf("connection = %v", out)
	}
	if _, has := out["tenant"]; has {
		t.Errorf("a Google connection shows a tenant: %v", out)
	}
	// The secret is stored at the organization's scope, where only the sign-in code reads it.
	if v, ok, err := f.svc.OrgSecret(ctx, org, "SSO_GOOGLE_CLIENT_SECRET"); err != nil || !ok || v != "g-client-secret" {
		t.Fatalf("stored secret = %q, %v, %v", v, ok, err)
	}

	// Reading: the list shows both the way and what is left to do, never the secret.
	rec, out = f.doH(t, "GET", url, "", bearer(f.ada))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || strings.Contains(rec.Body.String(), "g-client-secret") || out["max_connections"] != float64(2) {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", url+"/google", "", bearer(f.ada)); rec.Code != 200 || out["client_id"] != "123-abc.apps.googleusercontent.com" {
		t.Errorf("get: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", url+"/microsoft", "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("a connection that isn't there: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", url+"/apple", "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("a provider organizations don't have: %d", rec.Code)
	}

	// Replacing keeps the secret when none is sent, and changes it when one is.
	body := `{"client_id": "123-abc.apps.googleusercontent.com", "hosted_domain": "acme.example", "email_domains": ["acme.example"], "enabled": false}`
	if rec, out := f.doH(t, "PUT", url+"/google", body, f.withProof(t, f.ada)); rec.Code != 200 || out["enabled"] != false || out["client_secret_set"] != true {
		t.Fatalf("replace: %d %v", rec.Code, out)
	}
	if v, _, _ := f.svc.OrgSecret(ctx, org, "SSO_GOOGLE_CLIENT_SECRET"); v != "g-client-secret" {
		t.Errorf("the secret changed without being sent: %q", v)
	}
	if rec, _ := f.doH(t, "PUT", url+"/google", strings.Replace(body, `"enabled": false`, `"client_secret": "g-new"`, 1), f.withProof(t, f.ada)); rec.Code != 200 {
		t.Fatalf("replace with a secret: %d", rec.Code)
	}
	if v, _, _ := f.svc.OrgSecret(ctx, org, "SSO_GOOGLE_CLIENT_SECRET"); v != "g-new" {
		t.Errorf("the secret = %q", v)
	}

	// Microsoft: a tenant of its own, never one that admits anyone.
	ms := func(tenant string) string {
		return `{"client_id": "` + ssoTenant + `", "client_secret": "m-secret", "tenant": "` + tenant + `"}`
	}
	for _, tenant := range []string{"common", "organizations", "consumers", "acme"} {
		if rec, out := f.doH(t, "PUT", url+"/microsoft", ms(tenant), f.withProof(t, f.ada)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "tenant") {
			t.Errorf("tenant %s: %d %v", tenant, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "PUT", url+"/microsoft", ms(ssoTenant), f.withProof(t, f.ada)); rec.Code != 200 || out["tenant"] != ssoTenant {
		t.Fatalf("microsoft: %d %v", rec.Code, out)
	}

	// Refusals name the field.
	for name, tc := range map[string]struct{ provider, body, field string }{
		"no hosted domain":          {"google", `{"client_id": "c-1", "client_secret": "s"}`, "hosted_domain"},
		"a bad hosted domain":       {"google", `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "Acme"}`, "hosted_domain"},
		"a tenant on google":        {"google", `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "acme.example", "tenant": "` + ssoTenant + `"}`, "tenant"},
		"a hosted domain on ms":     {"microsoft", `{"client_id": "` + ssoTenant + `", "client_secret": "s", "tenant": "` + ssoTenant + `", "hosted_domain": "acme.example"}`, "hosted_domain"},
		"a client id that isn't":    {"microsoft", `{"client_id": "nope", "client_secret": "s", "tenant": "` + ssoTenant + `"}`, "client_id"},
		"an email domain":           {"google", `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "acme.example", "email_domains": ["a b"]}`, "email_domains"},
		"a role nobody declared":    {"google", `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "acme.example", "join_roles": ["wizard"]}`, "join_roles"},
		"an unknown field":          {"google", `{"client_id": "c-1", "hosted_domain": "acme.example", "issuer": "https://evil.example"}`, "issuer"},
		"a native id twice":         {"google", `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "acme.example", "native_client_ids": ["a1", "a1"]}`, "native_client_ids"},
		"a client secret too large": {"google", `{"client_id": "c-1", "client_secret": "` + strings.Repeat("x", 2000) + `", "hosted_domain": "acme.example"}`, "client_secret"},
	} {
		if rec, out := f.doH(t, "PUT", url+"/"+tc.provider, tc.body, f.withProof(t, f.ada)); rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "PUT", url+"/apple", `{"client_id": "c-1"}`, f.withProof(t, f.ada)); rec.Code != 404 {
		t.Errorf("apple: %d %v", rec.Code, out)
	}

	// Deleting needs a proof; the secret goes with the connection.
	if rec, out := f.doH(t, "DELETE", url+"/google", "", bearer(f.ada)); rec.Code != 401 || errCode(out) != "reauth_required" {
		t.Errorf("delete without a proof: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "DELETE", url+"/google", "", f.withProof(t, f.ada)); rec.Code != 204 {
		t.Fatalf("delete: %d %v", rec.Code, out)
	}
	if _, ok, _ := f.svc.OrgSecret(ctx, org, "SSO_GOOGLE_CLIENT_SECRET"); ok {
		t.Error("the secret outlived its connection")
	}
	if rec, _ := f.doH(t, "DELETE", url+"/google", "", f.withProof(t, f.ada)); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
	if _, ok, _ := f.svc.OrgSecret(ctx, org, "SSO_MICROSOFT_CLIENT_SECRET"); !ok {
		t.Error("deleting Google removed Microsoft's secret")
	}

	// Audited with the provider, never the secret.
	_, out = f.doH(t, "GET", "/v1/acme/_admin/audit?limit=100", "", jsonHdr(f.key))
	trail := toJSON(t, out)
	for _, want := range []string{"org.sso.set", "org.sso.deleted", "org:" + org} {
		if !strings.Contains(trail, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	for _, secret := range []string{"g-client-secret", "g-new", "m-secret"} {
		if strings.Contains(trail, secret) {
			t.Errorf("a client secret is in the audit trail: %q", secret)
		}
	}
}

// A manager who can only manage sign-in gives no more than they hold.
func TestConnectionJoinRolesFollowTheGrantingRules(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	org := f.orgOf(t, f.ada, "Acme Corp")
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"integrator"}); err != nil {
		t.Fatal(err)
	}
	url := orgsPath + "/" + org + "/sso/google"
	with := func(roles string) string {
		return `{"client_id": "c-1", "client_secret": "s", "hosted_domain": "acme.example", "join_roles": ` + roles + `}`
	}
	// Roles that open nothing, yes; the admin's capabilities the integrator lacks, no.
	if rec, out := f.doH(t, "PUT", url, with(`["editor", "member"]`), f.withProof(t, f.bob)); rec.Code != 200 {
		t.Fatalf("roles that open nothing: %d %v", rec.Code, out)
	}
	for _, roles := range []string{`["admin"]`, `["owner"]`, `["editor", "admin"]`} {
		if rec, out := f.doH(t, "PUT", url, with(roles), f.withProof(t, f.bob)); rec.Code != 403 {
			t.Errorf("%s: %d %v", roles, rec.Code, out)
		}
	}
	// The owner holds manage: roles and may give any.
	if rec, out := f.doH(t, "PUT", url, with(`["admin"]`), f.withProof(t, f.ada)); rec.Code != 200 {
		t.Errorf("an owner gives admin: %d %v", rec.Code, out)
	}
}

// Realm administrators manage any organization's connections; with the switch off all of it is 404.
func TestOrganizationConnectionsAsAnAdministrator(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	org := f.orgOf(t, f.ada, "Acme Corp")
	url := "/v1/acme/_admin/orgs/" + org + "/sso"
	key := jsonHdr(f.key)

	if rec, _ := f.doH(t, "GET", url, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", url, "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("a member is no administrator: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "PUT", url+"/google", ssoGoogle, key); rec.Code != 200 {
		t.Fatalf("set: %d", rec.Code)
	}
	// The admin may give any role (owner included).
	if rec, out := f.doH(t, "PUT", url+"/google", strings.Replace(ssoGoogle, `"editor"`, `"owner"`, 1), key); rec.Code != 200 {
		t.Errorf("admin gives owner: %d %v", rec.Code, out)
	}
	rec, out := f.doH(t, "GET", url, "", key)
	if items, _ := out["items"].([]any); rec.Code != 200 || len(items) != 1 || strings.Contains(rec.Body.String(), "g-client-secret") {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/_admin/orgs/nope/sso", "", key); rec.Code != 404 {
		t.Errorf("unknown organization: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", url+"/google", "", key); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", url+"/google", "", key); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
	if _, ok, _ := f.svc.OrgSecret(ctx, org, "SSO_GOOGLE_CLIENT_SECRET"); ok {
		t.Error("the secret outlived its connection")
	}

	// A deleted organization takes no connection.
	if _, err := f.svc.AdminDeleteOrg(ctx, org, "req"); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "PUT", url+"/google", ssoGoogle, key); rec.Code != 409 || errCode(out) != "org_not_active" {
		t.Errorf("deleted organization: %d %v", rec.Code, out)
	}

	// Switched off, the whole surface is a 404.
	f.svc.Settings.Organizations.SSO = nil
	other := f.orgOf(t, f.ada, "Other")
	for _, c := range []struct{ method, path string }{
		{"GET", orgsPath + "/" + other + "/sso"}, {"PUT", orgsPath + "/" + other + "/sso/google"}, {"DELETE", orgsPath + "/" + other + "/sso/google"},
	} {
		if rec, _ := f.doH(t, c.method, c.path, ssoGoogle, f.withProof(t, f.ada)); rec.Code != 404 {
			t.Errorf("%s %s with the switch off: %d", c.method, c.path, rec.Code)
		}
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/_admin/orgs/"+other+"/sso", "", key); rec.Code != 404 {
		t.Errorf("admin with the switch off: %d", rec.Code)
	}
}
