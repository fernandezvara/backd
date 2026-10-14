package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

func TestTransferADocument(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	projects := "/v1/acme/app/projects"
	adm := "/v1/acme/_admin/data/app/projects"
	orgA := f.orgOf(t, f.ada, "Alpha")
	orgB := f.orgOf(t, f.ada, "Beta")
	rec, out := f.doH(t, "POST", projects+"?org="+orgA, `{"title": "One"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	etag := rec.Header().Get("ETag")
	// Only through the admin data route: the ordinary routes have no transfer.
	if rec, _ := f.doH(t, "POST", projects+"/"+id+"/_transfer", `{"org": "`+orgB+`"}`, jsonHdr(f.ada)); rec.Code == 200 {
		t.Errorf("a user transferred: %d", rec.Code)
	}
	// Moves it, bumps the version and keeps the owner.
	rec, out = f.doH(t, "POST", adm+"/"+id+"/_transfer", `{"org": "`+orgB+`"}`, jsonHdr(f.bob))
	meta, _ := out["_meta"].(map[string]any)
	if rec.Code != 200 || meta["org"] != orgB || meta["version"] != float64(2) || meta["owner"] != f.adaID {
		t.Fatalf("transfer: %d %v", rec.Code, out)
	}
	// Members of the new organization see it, those of the old one don't.
	_, list := f.doH(t, "GET", projects+"?org="+orgB, "", bearer(f.ada))
	if len(list["items"].([]any)) != 1 {
		t.Errorf("in the new organization: %v", list)
	}
	_, list = f.doH(t, "GET", projects+"?org="+orgA, "", bearer(f.ada))
	if len(list["items"].([]any)) != 0 {
		t.Errorf("in the old organization: %v", list)
	}
	// If-Match is honored.
	h := jsonHdr(f.bob)
	h["If-Match"] = etag
	if rec, _ := f.doH(t, "POST", adm+"/"+id+"/_transfer", `{"org": "`+orgA+`"}`, h); rec.Code != 412 {
		t.Errorf("stale If-Match: %d", rec.Code)
	}
	for name, c := range map[string]struct {
		body string
		code int
		err  string
	}{
		"unknown organization": {`{"org": "nosuch"}`, 404, "not_found"},
		"null on required":     {`{"org": null}`, 400, "org_required"},
		"no org":               {`{}`, 400, "validation_error"},
		"a team":               {`{"org": "` + orgA + `", "team": "t1"}`, 400, "team_not_allowed"},
		"unknown field":        {`{"org": "` + orgA + `", "x": 1}`, 400, "validation_error"},
	} {
		rec, out := f.doH(t, "POST", adm+"/"+id+"/_transfer", c.body, jsonHdr(f.bob))
		if rec.Code != c.code || errCode(out) != c.err {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if rec, _ := f.doH(t, "POST", adm+"/nosuch/_transfer", `{"org": "`+orgA+`"}`, jsonHdr(f.bob)); rec.Code != 404 {
		t.Errorf("unknown document: %d", rec.Code)
	}
	// A collection without organizations refuses; an optional one lets a document leave.
	if rec, out := f.doH(t, "POST", "/v1/acme/_admin/data/app/posts/x/_transfer", `{"org": "`+orgA+`"}`, jsonHdr(f.bob)); rec.Code != 400 || errCode(out) != "org_not_allowed" {
		t.Errorf("posts: %d %v", rec.Code, out)
	}
	_, out = f.doH(t, "POST", "/v1/acme/app/shared?org="+orgA, `{"title": "S"}`, jsonHdr(f.ada))
	sid := out["id"].(string)
	rec, out = f.doH(t, "POST", "/v1/acme/_admin/data/app/shared/"+sid+"/_transfer", `{"org": null}`, jsonHdr(f.bob))
	if rec.Code != 200 || out["_meta"].(map[string]any)["org"] != nil {
		t.Errorf("out of its organization: %d %v", rec.Code, out)
	}
	// A deleted organization can't receive.
	if _, err := f.svc.Store.UpdateOrg(context.Background(), orgB, auth.OrgUpdate{Status: auth.OrgDeleted}, f.svc.Clock()); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "POST", adm+"/"+id+"/_transfer", `{"org": "`+orgB+`"}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "org_not_active" {
		t.Errorf("deleted: %d %v", rec.Code, out)
	}
	// The audit trail names both organizations, never content.
	trail, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditDataTransfer})
	found := false
	for _, rec := range trail {
		if rec.Details["collection"] == "projects" {
			found = rec.Details["from"] == orgA && rec.Details["to"] == orgB && !strings.Contains(rec.Target, "One")
		}
	}
	if !found {
		t.Errorf("audit: %+v", trail)
	}
}

func TestAssignOrganizationsToExistingDocuments(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	org := f.orgOf(t, f.ada, "Adopt")
	adm := "/v1/acme/_admin/data/app/shared"
	// Documents made before the organization existed: through the repository, without one.
	coll, _ := f.reg.Collection("acme", "app", "shared")
	repo := f.store.Repository(coll)
	for i, title := range []string{"a", "b", "c"} {
		doc := map[string]any{"id": "old" + string(rune('0'+i)), "title": title, "_meta": map[string]any{"created_at": f.clock.UTC(), "updated_at": f.clock.UTC(), "version": int64(1)}}
		if err := repo.Create(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	if rec, out := f.doH(t, "POST", adm+"/_assign_org?dry_run=true", `{"org": "`+org+`", "where": {"title": {"$in": ["a", "b"]}}}`, jsonHdr(f.bob)); rec.Code != 200 || out["matched"] != float64(2) {
		t.Fatalf("dry run: %d %v", rec.Code, out)
	}
	rec, out := f.doH(t, "POST", adm+"/_assign_org", `{"org": "`+org+`", "where": {"title": {"$in": ["a", "b"]}}}`, jsonHdr(f.bob))
	if rec.Code != 202 || out["matched"] != float64(2) || out["job"] == nil {
		t.Fatalf("start: %d %v", rec.Code, out)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("no job")
	}
	got, _ := repo.List(ctx, storage.Query{Limit: 100})
	assigned := 0
	for _, d := range got.Items {
		if m, _ := d["_meta"].(map[string]any); m["org"] == org {
			assigned++
			if m["version"] != int64(2) {
				t.Errorf("version: %v", m["version"])
			}
		}
	}
	if assigned != 2 {
		t.Errorf("assigned %d documents, want 2", assigned)
	}
	// What has an organization is never moved by a later assignment.
	other := f.orgOf(t, f.ada, "Other")
	if rec, out := f.doH(t, "POST", adm+"/_assign_org?dry_run=true", `{"org": "`+other+`"}`, jsonHdr(f.bob)); rec.Code != 200 || out["matched"] != float64(1) {
		t.Errorf("remaining: %d %v", rec.Code, out)
	}
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"no org": {`{}`, 400}, "null": {`{"org": null}`, 400}, "unknown": {`{"org": "nosuch"}`, 404},
		"bad where": {`{"org": "` + org + `", "where": {"nope": 1}}`, 400}, "a team": {`{"org": "` + org + `", "team": "x"}`, 400},
	} {
		if rec, out := f.doH(t, "POST", adm+"/_assign_org", c.body, jsonHdr(f.bob)); rec.Code != c.code {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if rec, _ := f.doH(t, "POST", "/v1/acme/_admin/data/app/posts/_assign_org", `{"org": "`+org+`"}`, jsonHdr(f.bob)); rec.Code != 400 {
		t.Errorf("a collection without organizations: %d", rec.Code)
	}
}
