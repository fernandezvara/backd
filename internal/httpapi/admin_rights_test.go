package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// roles gives bob exactly these roles (and takes away the others he held).
func (f *rulesFixture) bobHolds(t *testing.T, roles ...string) {
	t.Helper()
	ctx := context.Background()
	for _, r := range []string{"staff", "support", "keeper", "auditor", "runner"} {
		_ = f.svc.RemoveRole(ctx, "bob@example.com", r)
	}
	for _, r := range roles {
		if err := f.svc.AddRole(ctx, "bob@example.com", r); err != nil {
			t.Fatal(err)
		}
	}
}

func jsonHdr(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
}

// Each area of the admin API opens only for roles that hold it; an
// administrator holding another area gets 403 naming what was missing.
func TestAdminRightsPerEndpoint(t *testing.T) {
	f := newRulesFixture(t)
	areas := []struct {
		name, method, path, body string
	}{
		{"users", "GET", "/users", ""},
		{"invitations", "GET", "/invitations", ""},
		{"apikeys", "GET", "/apikeys", ""},
		{"secrets", "GET", "/secrets", ""},
		{"audit", "GET", "/audit", ""},
		{"functions", "GET", "/jobs", ""},
		{"functions", "GET", "/invocations", ""},
	}
	holds := map[string][]string{
		"staff":   {"users", "invitations", "apikeys", "secrets", "audit", "functions"},
		"support": {"users", "invitations"},
		"keeper":  {"apikeys", "secrets"},
		"auditor": {"audit"},
		"runner":  {"functions"},
	}
	for role, rights := range holds {
		f.bobHolds(t, role)
		for _, e := range areas {
			rec, out := f.doH(t, e.method, admin+e.path, e.body, bearer(f.bob))
			should := false
			for _, r := range rights {
				should = should || r == e.name
			}
			switch {
			case should && rec.Code == http.StatusForbidden:
				t.Errorf("%s: %s %s refused (%d %v), but the role holds %q", role, e.method, e.path, rec.Code, out, e.name)
			case !should && rec.Code != http.StatusForbidden:
				t.Errorf("%s: %s %s answered %d, want 403: the role doesn't hold %q", role, e.method, e.path, rec.Code, e.name)
			case !should && !strings.Contains(out["error"].(map[string]any)["message"].(string), `"`+e.name+`"`):
				t.Errorf("%s: the 403 for %s doesn't name %q: %v", role, e.path, e.name, out)
			}
		}
	}
	// An admin API key opens everything; so does a session with two roles' worth.
	for _, e := range areas {
		if rec, out := f.doH(t, e.method, admin+e.path, e.body, bearer(f.key)); rec.Code == http.StatusForbidden {
			t.Errorf("admin key refused on %s: %v", e.path, out)
		}
	}
	f.bobHolds(t, "support", "auditor")
	if rec, _ := f.doH(t, "GET", admin+"/audit", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("rights of two roles add up: audit answered %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", admin+"/secrets", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("…and only add up: secrets answered %d", rec.Code)
	}
	// Refusals are in the audit trail, with what was missing.
	_, out := f.doH(t, "GET", admin+"/audit?action=admin.refused&limit=100", "", bearer(f.key))
	items, _ := out["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no admin.refused in the audit trail: %v", out)
	}
	found := false
	for _, it := range items {
		d, _ := it.(map[string]any)["details"].(map[string]any)
		if r, _ := d["reason"].(string); strings.Contains(r, `needs the "secrets" admin right`) && d["path"] == admin+"/secrets" {
			found = true
		}
	}
	if !found {
		t.Errorf("the secrets refusal isn't audited with its reason: %v", items)
	}
}

// Nobody gets more than they hold by managing someone else: a role is
// granted only by someone holding all it opens, and users who hold more
// than the caller can't be changed.
func TestAdminRightsNoEscalation(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	refused := func(what string, rec int, out map[string]any) {
		t.Helper()
		if rec != http.StatusForbidden {
			t.Errorf("%s: %d %v, want 403", what, rec, out)
		}
	}
	f.bobHolds(t, "support")

	// A support admin grants roles that open no more than they hold…
	if rec, out := f.doH(t, "PUT", admin+"/users/"+f.adaID+"/roles/support", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Fatalf("granting support: %d %v", rec.Code, out)
	}
	// …and a role that is no admin role at all.
	if rec, out := f.doH(t, "PUT", admin+"/users/"+f.adaID+"/roles/admin", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("granting a plain role: %d %v", rec.Code, out)
	}
	// Not one that opens more: themselves included.
	for _, id := range []string{f.adaID, f.bobID} {
		rec, out := f.doH(t, "PUT", admin+"/users/"+id+"/roles/staff", "", bearer(f.bob))
		refused("granting staff to "+id, rec.Code, out)
		rec, out = f.doH(t, "PUT", admin+"/users/"+id+"/roles/keeper", "", bearer(f.bob))
		refused("granting keeper to "+id, rec.Code, out)
	}
	if err := f.svc.AddRole(ctx, "ada@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	// Ada now holds staff: a support admin may read her, nothing more.
	if rec, out := f.doH(t, "GET", admin+"/users/"+f.adaID, "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("reading a more powerful user: %d %v", rec.Code, out)
	}
	for _, c := range []struct{ name, method, path, body string }{
		{"password", "POST", "/users/" + f.adaID + "/password", `{"password":"a long enough password"}`},
		{"address", "POST", "/users/" + f.adaID + "/email", `{"email":"ada2@example.com"}`},
		{"disable", "PATCH", "/users/" + f.adaID, `{"disabled":true}`},
		{"erase", "DELETE", "/users/" + f.adaID, ""},
		{"networks", "PUT", "/users/" + f.adaID + "/networks", `{"admin_networks":[],"login_networks":[]}`},
		{"remove a role", "DELETE", "/users/" + f.adaID + "/roles/staff", ""},
		{"remove their own power over them", "DELETE", "/users/" + f.adaID + "/roles/support", ""},
	} {
		rec, out := f.doH(t, c.method, admin+c.path, c.body, jsonHdr(f.bob))
		refused(c.name+" of a user holding more", rec.Code, out)
	}
	// A full administrator, and the admin key, may do all of that.
	f.bobHolds(t, "staff")
	if rec, out := f.doH(t, "PUT", admin+"/users/"+f.adaID+"/roles/keeper", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("staff granting keeper: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/roles/keeper", "", bearer(f.key)); rec.Code != http.StatusOK {
		t.Errorf("the admin key removing keeper: %d %v", rec.Code, out)
	}

	// API keys: an admin key opens every area, so only a full administrator
	// creates or revokes one.
	f.bobHolds(t, "keeper")
	rec, out := f.doH(t, "POST", admin+"/apikeys", `{"name":"mine","role":"admin"}`, jsonHdr(f.bob))
	refused("a keeper creating an admin key", rec.Code, out)
	if rec, out := f.doH(t, "POST", admin+"/apikeys", `{"name":"mine","role":"data"}`, jsonHdr(f.bob)); rec.Code != http.StatusCreated {
		t.Errorf("a keeper creating a data key: %d %v", rec.Code, out)
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, "root", auth.KeyOptions{Role: auth.KeyRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	rec, out = f.doH(t, "DELETE", admin+"/apikeys/root", "", bearer(f.bob))
	refused("a keeper revoking an admin key", rec.Code, out)
	if rec, _ := f.doH(t, "DELETE", admin+"/apikeys/mine", "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("a keeper revoking a data key: %d", rec.Code)
	}
	f.bobHolds(t, "staff")
	if rec, out := f.doH(t, "POST", admin+"/apikeys", `{"name":"second","role":"admin"}`, jsonHdr(f.bob)); rec.Code != http.StatusCreated {
		t.Errorf("a full administrator creating an admin key: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/apikeys/root", "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("a full administrator revoking one: %d", rec.Code)
	}
}
