package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// Realm administrators write an organization's secrets and see them listed without a value;
// nothing in the API hands one back.
func TestAdminOrganizationSecrets(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.svc.Store.CreateOrg(ctx, auth.Org{ID: "org1", Name: "Acme", Status: auth.OrgActive}, auth.OrgMember{Org: "org1", User: "u1"}); err != nil {
		t.Fatal(err)
	}
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	url := admin + "/secrets/SSO_GOOGLE_CLIENT_SECRET"

	// Needs the secrets right, like every secret.
	f.bobHolds(t, "auditor")
	if rec, _ := f.doH(t, "PUT", url, `{"value":"s3cret-org","org":"org1"}`, bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("without the secrets right: %d", rec.Code)
	}

	for body, want := range map[string]string{
		`{"value":"x","org":"nope"}`:                  "not an organization",
		`{"value":"x","org":"org1","database":"app"}`: "no database",
		`{"value":"","org":"org1"}`:                   "must not be empty",
	} {
		rec, out := f.doH(t, "PUT", url, body, key)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %v", body, rec.Code, out)
		}
	}

	if rec, out := f.doH(t, "PUT", url, `{"value":"s3cret-org","org":"org1"}`, key); rec.Code != http.StatusNoContent {
		t.Fatalf("set: %d %v", rec.Code, out)
	}
	if v, ok, err := f.svc.OrgSecret(ctx, "org1", "SSO_GOOGLE_CLIENT_SECRET"); err != nil || !ok || v != "s3cret-org" {
		t.Fatalf("stored = %q, %v, %v", v, ok, err)
	}
	// The realm scope of the same name is another secret, still unset.
	if _, err := f.svc.Store.SecretByScope(ctx, "", "SSO_GOOGLE_CLIENT_SECRET"); err == nil {
		t.Error("the organization's secret was also stored at the realm")
	}

	rec, out := f.doH(t, "GET", admin+"/secrets", "", key)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-org") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	if it := items[0].(map[string]any); it["org"] != "org1" || it["database"] != "" || it["name"] != "SSO_GOOGLE_CLIENT_SECRET" {
		t.Errorf("item = %v", it)
	}

	// Deleting needs the scope: without ?org= the realm scope has nothing to delete.
	if rec, _ := f.doH(t, "DELETE", url, "", key); rec.Code != http.StatusNotFound {
		t.Errorf("delete at the realm scope: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", url+"?org=org1&database=app", "", key); rec.Code != http.StatusBadRequest {
		t.Errorf("both scopes: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", url+"?org=org1", "", key); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", url+"?org=org1", "", key); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}

	// Both writes are audited with the organization, never the value.
	_, out = f.doH(t, "GET", admin+"/audit?limit=100", "", key)
	b := toJSON(t, out)
	if !strings.Contains(b, "secret.set") || !strings.Contains(b, "secret:org:org1/SSO_GOOGLE_CLIENT_SECRET") || strings.Contains(b, "s3cret-org") {
		t.Errorf("audit = %s", b)
	}
}
