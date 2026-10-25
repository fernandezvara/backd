package httpapi

import (
	"context"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

func TestDeleteAnOrganization(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	shared := "/v1/acme/app/shared"
	org := f.orgOf(t, f.ada, "Doomed")
	other := f.orgOf(t, f.ada, "Safe")
	// `delete` and `keep` collections, one document each, and one in another organization.
	for path, body := range map[string]string{
		projects + "?org=" + org: `{"title": "goes"}`, projects + "?org=" + other: `{"title": "stays"}`, shared + "?org=" + org: `{"title": "kept"}`,
	} {
		if rec, out := f.doH(t, "POST", path, body, jsonHdr(f.ada)); rec.Code != 201 {
			t.Fatalf("create %s: %d %v", path, rec.Code, out)
		}
	}
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	// The report says what will happen before it does.
	rec, out := f.doH(t, "GET", "/v1/acme/_admin/orgs/"+org+"/owned", "", bearer(f.bob))
	if rec.Code != 200 || out["to_delete"] != float64(1) || out["to_keep"] != float64(1) {
		t.Fatalf("owned: %d %v", rec.Code, out)
	}
	// A manager needs a fresh proof; a member who can't manage is refused first.
	if rec, _ := f.doH(t, "DELETE", orgsPath+"/"+org, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("an editor deletes: %d", rec.Code)
	}
	if rec, out := f.doH(t, "DELETE", orgsPath+"/"+org, "", bearer(f.ada)); rec.Code != 401 || errCode(out) != "reauth_required" {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "DELETE", orgsPath+"/"+org, "", f.withProof(t, f.ada))
	if rec.Code != 204 {
		t.Fatalf("delete: %d %v", rec.Code, out)
	}
	// Access ends at once: nobody is a member any more.
	for _, who := range []string{f.ada, f.bob} {
		if rec, out := f.doH(t, "GET", orgsPath+"/"+org, "", bearer(who)); rec.Code != 403 || errCode(out) != "not_org_member" {
			t.Errorf("after deleting: %d %v", rec.Code, out)
		}
	}
	if rec, _ := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(f.ada)); rec.Code != 403 {
		t.Errorf("create in a deleted organization: %d", rec.Code)
	}
	// The record stays, with no slug, and the cleanup follows.
	rec, out = f.doH(t, "GET", "/v1/acme/_admin/orgs/"+org, "", bearer(f.bob))
	if rec.Code != 200 || out["status"] != "deleted" {
		t.Errorf("record: %d %v", rec.Code, out)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("no cleanup job")
	}
	coll, _ := f.reg.Collection("acme", "app", "projects")
	repo := f.store.Repository(coll)
	if n, _ := repo.(interface {
		CountInOrg(context.Context, string) (int64, error)
	}).CountInOrg(ctx, org); n != 0 {
		t.Errorf("documents left in a delete collection: %d", n)
	}
	if n, _ := repo.(interface {
		CountInOrg(context.Context, string) (int64, error)
	}).CountInOrg(ctx, other); n != 1 {
		t.Errorf("another organization's documents: %d", n)
	}
	kept, _ := f.reg.Collection("acme", "app", "shared")
	if n, _ := f.store.Repository(kept).(interface {
		CountInOrg(context.Context, string) (int64, error)
	}).CountInOrg(ctx, org); n != 1 {
		t.Errorf("kept documents: %d", n)
	}
	// Deleting twice, and the admin route.
	if rec, out := f.doH(t, "DELETE", "/v1/acme/_admin/orgs/"+org, "", bearer(f.bob)); rec.Code != 409 || errCode(out) != "org_not_active" {
		t.Errorf("delete again: %d %v", rec.Code, out)
	}
	// Reactivating: no members; an owner is assigned.
	if rec, out := f.doH(t, "POST", "/v1/acme/_admin/orgs/"+other+"/reactivate", `{}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "org_not_deleted" {
		t.Errorf("reactivate an active one: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", "/v1/acme/_admin/orgs/"+org+"/reactivate", `{}`, jsonHdr(f.bob))
	if rec.Code != 200 || out["status"] != "active" {
		t.Fatalf("reactivate: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", orgsPath+"/"+org, "", bearer(f.ada)); rec.Code != 403 {
		t.Errorf("a reactivated organization has no members: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", "/v1/acme/_admin/orgs/"+org+"/owner", `{"user": "`+f.adaID+`"}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("assign owner: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", shared+"?org="+org, "", bearer(f.ada)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("kept documents are back: %d %v", rec.Code, out)
	}
}

func TestDeletedOrganizationsGiveUpTheirSlug(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	admin := "/v1/acme/_admin/orgs"
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "One", "slug": "shared-name"}`, jsonHdr(f.ada))
	first := out["id"].(string)
	if rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/"+first, "", bearer(f.bob)); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	// The slug is free for another organization, and deleted ones still count against the cap.
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Two", "slug": "shared-name"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Errorf("reuse the slug: %d %v", rec.Code, out)
	}
	// Reactivating the first needs another slug: its own is taken.
	if rec, out := f.doH(t, "POST", admin+"/"+first+"/reactivate", `{}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "slug_taken" {
		t.Errorf("reactivate with the slug taken: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", admin+"/"+first+"/reactivate", `{"slug": "first-again"}`, jsonHdr(f.bob)); rec.Code != 200 || out["slug"] != "first-again" {
		t.Errorf("reactivate with another: %d %v", rec.Code, out)
	}
	f.orgOf(t, f.ada, "Three")
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Four"}`, jsonHdr(f.ada)); rec.Code != 409 || errCode(out) != "org_limit_reached" {
		t.Errorf("the cap counts deleted ones: %d %v", rec.Code, out)
	}
}

// Erasing a user takes them out of organizations; their documents there stay with the owner
// cleared, and an organization left without a manager becomes orphaned.
func TestEraseAUserInOrganizations(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	alone := f.orgOf(t, f.ada, "Alone") // ada is the only manager
	shared := f.orgOf(t, f.ada, "Pair") // two managers
	if _, err := f.svc.AdminAddMember(ctx, shared, f.bobID, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{alone, shared} {
		if rec, out := f.doH(t, "POST", projects+"?org="+o, `{"title": "kept"}`, jsonHdr(f.ada)); rec.Code != 201 {
			t.Fatalf("create: %d %v", rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "POST", "/v1/acme/app/ztally?org="+shared, `{"title": "entry"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Fatalf("ledger: %d %v", rec.Code, out)
	}
	// An invitation she made, and one made for her.
	if rec, out := f.doH(t, "POST", orgsPath+"/"+shared+"/invitations", `{"binding": "user", "user": "`+f.adaID+`"}`, jsonHdr(f.bob)); rec.Code != 201 {
		t.Fatalf("invitation for ada: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", orgsPath+"/"+shared+"/invitations", `{"binding": "none"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Fatalf("invitation by ada: %d %v", rec.Code, out)
	}
	// The report names what is going to happen.
	rec, out := f.doH(t, "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", "", bearer(f.bob))
	if rec.Code != 200 {
		t.Fatalf("owned: %d %v", rec.Code, out)
	}
	orgs := out["organizations"].([]any)
	last := map[string]bool{}
	for _, o := range orgs {
		m := o.(map[string]any)
		last[m["id"].(string)] = m["last_manager"].(bool)
	}
	if len(orgs) != 2 || !last[alone] || last[shared] {
		t.Errorf("organizations: %v", orgs)
	}
	kept := out["kept_in_organizations"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["documents"] != float64(2) {
		t.Errorf("kept: %v", kept)
	}
	// Erase her and let the worker run.
	if rec, out := f.doH(t, "DELETE", "/v1/acme/_admin/users/"+f.adaID, "", bearer(f.bob)); rec.Code != 202 {
		t.Fatalf("erase: %d %v", rec.Code, out)
	}
	for w.RunOnce(ctx) {
	}
	// Alone is orphaned (no members left); Pair still has bob and stays active.
	one, err := f.svc.Store.OrgByID(ctx, alone)
	if err != nil || one.Status != "orphaned" || one.CreatedBy != "" {
		t.Errorf("alone: %+v %v", one, err)
	}
	pair, _ := f.svc.Store.OrgByID(ctx, shared)
	if pair.Status != "active" {
		t.Errorf("pair: %+v", pair)
	}
	// Her documents stay, with no owner.
	coll, _ := f.reg.Collection("acme", "app", "projects")
	got, _ := f.store.Repository(coll).List(ctx, storage.Query{Limit: 50})
	if len(got.Items) != 2 {
		t.Fatalf("documents: %d", len(got.Items))
	}
	for _, d := range got.Items {
		if m, _ := d["_meta"].(map[string]any); m["owner"] != nil || m["org"] == nil {
			t.Errorf("document after the erase: %v", d)
		}
	}
	// A collection with in_org: apply treats them like any other: they were deleted.
	tally, _ := f.reg.Collection("acme", "app", "ztally")
	left, _ := f.store.Repository(tally).List(ctx, storage.Query{Limit: 50})
	if len(left.Items) != 0 {
		t.Errorf("tally after the erase: %d", len(left.Items))
	}
	// Bob reads the shared organization's document.
	if rec, out := f.doH(t, "GET", projects+"?org="+shared, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("bob lists: %d %v", rec.Code, out)
	}
	// The invitations: the one for her is gone, the one she made stays with no creator.
	invs, _ := f.svc.Store.ListOrgInvitations(ctx, shared)
	if len(invs) != 1 || invs[0].CreatedBy != "" {
		t.Errorf("invitations: %+v", invs)
	}
	// An administrator assigns a manager to the orphaned one.
	if rec, out := f.doH(t, "POST", "/v1/acme/_admin/orgs/"+alone+"/owner", `{"user": "`+f.bobID+`"}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("assign: %d %v", rec.Code, out)
	}
	back, _ := f.svc.Store.OrgByID(ctx, alone)
	if back.Status != "active" {
		t.Errorf("after assigning: %+v", back)
	}
}

// A deleted organization's sign-in connections and the secrets kept for them go with it.
func TestDeleteAnOrganizationRemovesItsConnections(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	f.svc.Realm = "acme"
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	doomed := f.orgOf(t, f.ada, "Doomed")
	safe := f.orgOf(t, f.ada, "Safe")
	for _, org := range []string{doomed, safe} {
		for provider, in := range map[string]auth.ConnectionInput{
			"google":    {ClientID: "c-1", ClientSecret: "g-secret", HostedDomain: "acme.example"},
			"microsoft": {ClientID: "11111111-2222-3333-4444-555555555555", ClientSecret: "m-secret", Tenant: "11111111-2222-3333-4444-555555555555"},
		} {
			if _, err := f.svc.AdminSetOrgConnection(ctx, org, provider, in); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A secret left behind by an interrupted removal is found by its organization, not its connection.
	if err := f.svc.SetOrgSecret(ctx, doomed, "SSO_STRAY", "stray", "test"); err != nil {
		t.Fatal(err)
	}

	if rec, out := f.doH(t, "DELETE", "/v1/acme/_admin/orgs/"+doomed, "", bearer(f.bob)); rec.Code != 204 {
		t.Fatalf("delete: %d %v", rec.Code, out)
	}
	// Unavailable at once: the connections are still stored, but nobody can sign in through them.
	if c, err := f.svc.Store.OrgConnection(ctx, doomed, "google"); err != nil || !c.Enabled {
		t.Fatalf("the connection before the job: %+v %v", c, err)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("no cleanup job")
	}
	for _, provider := range []string{"google", "microsoft"} {
		if _, err := f.svc.Store.OrgConnection(ctx, doomed, provider); err == nil {
			t.Errorf("the %s connection survived the organization", provider)
		}
	}
	secrets, _ := f.svc.Store.ListSecrets(ctx)
	kept := 0
	for _, s := range secrets {
		switch s.Org {
		case doomed:
			t.Errorf("a secret of the deleted organization is left: %s", s.Name)
		case safe:
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("the other organization has %d secrets, want 2", kept)
	}
	if list, _ := f.svc.Store.OrgConnections(ctx, safe); len(list) != 2 {
		t.Errorf("the other organization's connections: %d", len(list))
	}
	// The cleanup says how many, and the job is repeatable.
	entries, _, err := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditOrgDeleted, Limit: 5})
	if err != nil || len(entries) != 1 || entries[0].Details["connections"] != 2 {
		t.Fatalf("audit: %+v %v", entries, err)
	}
	if n, err := f.svc.RemoveOrgConnections(ctx, doomed); err != nil || n != 0 {
		t.Errorf("a second removal: %d %v", n, err)
	}
	// A reactivated organization has no connections: it is set up again.
	if rec, out := f.doH(t, "POST", "/v1/acme/_admin/orgs/"+doomed+"/reactivate", `{}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Fatalf("reactivate: %d %v", rec.Code, out)
	}
	if list, _ := f.svc.Store.OrgConnections(ctx, doomed); len(list) != 0 {
		t.Errorf("a reactivated organization has connections: %+v", list)
	}
}
