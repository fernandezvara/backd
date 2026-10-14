package httpapi

import (
	"context"
	"testing"
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
