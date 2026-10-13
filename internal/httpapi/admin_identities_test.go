package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAdminSeesAndUnlinksIdentities(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	// Bob signs in with Apple and keeps his password.
	f.idp.GiveRefreshToken("refresh-bob")
	now := time.Now()
	if err := f.svc.Store.PutIdentity(ctx, auth.Identity{ID: "i-bob-g", UserID: f.bobID, Provider: "google", Subject: "g-bob", Email: "bob.work@gmail.example", EmailVerified: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	code, out := f.as(t, f.key, "GET", "/v1/acme/_admin/users/"+f.bobID+"/identities", "")
	items, _ := out["items"].([]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["provider"] != "password" || items[1].(map[string]any)["provider"] != "google" || items[1].(map[string]any)["email"] != "bob.work@gmail.example" {
		t.Fatalf("list: %d %v", code, out)
	}
	for _, it := range items {
		for k := range it.(map[string]any) {
			if k == "subject" || k == "password_hash" || k == "apple_refresh_token" {
				t.Errorf("the list shows %s", k)
			}
		}
	}
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.bobID+"/identities/google", ""); code != 204 {
		t.Fatalf("unlink: %d", code)
	}
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditIdentityUnlinked})
	if len(recs) != 1 || recs[0].Target != "user:"+f.bobID || recs[0].Details["provider"] != "google" || recs[0].Details["by"] != "admin" || recs[0].Actor == "user:"+f.bobID {
		t.Errorf("audit: %+v", recs)
	}
	// The password is now his only way in: an administrator can't lock him out either.
	if code, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.bobID+"/identities/password", ""); code != 409 || errCode(out) != "last_sign_in_method" {
		t.Errorf("last method: %d %v", code, out)
	}
	// Someone who isn't an administrator can't see or change any of it.
	if code, _ := f.as(t, f.bob, "GET", "/v1/acme/_admin/users/"+f.bobID+"/identities", ""); code != 403 {
		t.Errorf("a user listing their own through the admin API: %d", code)
	}
}
