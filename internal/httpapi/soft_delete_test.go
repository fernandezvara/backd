package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

const bin = "/v1/acme/app/bin"

func jsonAs(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
}

func itemTitles(out map[string]any) string {
	var titles []string
	items, _ := out["items"].([]any)
	for _, it := range items {
		titles = append(titles, it.(map[string]any)["title"].(string))
	}
	slices.Sort(titles) // the fake store doesn't sort
	return strings.Join(titles, ",")
}

// A collection with soft_delete: DELETE hides the document, the trash is
// reached with deleted=only|include under the restore rule, restore brings it
// back and purge removes it for good under the purge rule.
func TestSoftDelete(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	create := func(token, title string) string {
		t.Helper()
		rec, out := f.doH(t, "POST", bin, `{"title":"`+title+`"}`, jsonAs(token))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", title, rec.Code, out)
		}
		return out["id"].(string)
	}
	one := create(f.ada, "one")
	create(f.ada, "two")
	create(f.bob, "bobs")

	// DELETE answers as before and the document disappears from every read.
	if rec, out := f.doH(t, "DELETE", bin+"/"+one, "", bearer(f.ada)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", bin+"/"+one, "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("get a deleted document: %d", rec.Code)
	}
	_, out := f.doH(t, "GET", bin+"?order_by=title&count=true", "", bearer(f.ada))
	if itemTitles(out) != "bobs,two" || out["total"] != float64(2) {
		t.Errorf("list hides the deleted one: %v", out)
	}
	if rec, _ := f.doH(t, "PATCH", bin+"/"+one, `{"title":"x"}`, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/merge-patch+json"}); rec.Code != http.StatusNotFound {
		t.Errorf("patch a deleted document: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", bin+"/"+one, "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("delete a deleted document: %d", rec.Code)
	}

	// The trash: marks, and who may see it (the restore rule).
	_, out = f.doH(t, "GET", bin+"?deleted=only", "", bearer(f.ada))
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("trash of ada: %v", out)
	}
	meta := items[0].(map[string]any)["_meta"].(map[string]any)
	deletedAt, err := time.Parse(time.RFC3339Nano, meta["deleted_at"].(string))
	if err != nil || meta["deleted_by"] != "user:"+f.adaID || meta["version"] != float64(2) {
		t.Errorf("deletion marks: %v (%v)", meta, err)
	}
	if purge, err := time.Parse(time.RFC3339Nano, meta["purge_at"].(string)); err != nil || purge.Sub(deletedAt) != 7*24*time.Hour {
		t.Errorf("purge_at should be 7d after deleted_at: %v", meta)
	}
	if _, out := f.doH(t, "GET", bin+"?deleted=only", "", bearer(f.bob)); itemTitles(out) != "" {
		t.Errorf("bob sees ada's trash: %v", out)
	}
	if _, out := f.doH(t, "GET", bin+"?deleted=include&order_by=title", "", bearer(f.ada)); itemTitles(out) != "bobs,one,two" {
		t.Errorf("include: %v", out)
	}
	if rec, _ := f.doH(t, "GET", bin+"/"+one+"?deleted=only", "", bearer(f.ada)); rec.Code != http.StatusOK {
		t.Errorf("get from the trash: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", bin+"/"+one+"?deleted=only", "", bearer(f.bob)); rec.Code != http.StatusNotFound {
		t.Errorf("bob gets ada's deleted document: %d", rec.Code)
	}
	// Queries can filter on the marks.
	if _, out := f.doH(t, "GET", bin+`?deleted=include&where={"_meta.deleted_at":{"$isNull":false}}`, "", bearer(f.ada)); itemTitles(out) != "one" {
		t.Errorf("where on deleted_at: %v", out)
	}

	// Bad ask for the trash.
	for _, q := range []string{"?deleted=yes", "?deleted="} {
		if rec, out := f.doH(t, "GET", bin+q, "", bearer(f.ada)); rec.Code != http.StatusBadRequest || errCode(out) != "invalid_query" {
			t.Errorf("%s: %d %v", q, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "GET", posts+"?deleted=only", "", bearer(f.ada)); rec.Code != http.StatusBadRequest || !strings.Contains(string(rec.Body.Bytes()), "doesn't soft-delete") {
		t.Errorf("deleted= on a collection that doesn't soft-delete: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", posts+"/x/restore", "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("restore on a collection that doesn't soft-delete: %d", rec.Code)
	}

	// Restore: only who the restore rule lets see it; If-Match is honoured.
	if rec, _ := f.doH(t, "POST", bin+"/"+one+"/restore", "", bearer(f.bob)); rec.Code != http.StatusNotFound {
		t.Errorf("bob restoring ada's document: %d", rec.Code)
	}
	hdr := bearer(f.ada)
	hdr["If-Match"] = `"1"`
	if rec, _ := f.doH(t, "POST", bin+"/"+one+"/restore", "", hdr); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("restore with a stale If-Match: %d", rec.Code)
	}
	rec, out := f.doH(t, "POST", bin+"/"+one+"/restore", "", bearer(f.ada))
	rmeta, _ := out["_meta"].(map[string]any)
	if rec.Code != http.StatusOK || out["title"] != "one" || rmeta["version"] != float64(3) || rec.Header().Get("ETag") != `"3"` || rmeta["deleted_at"] != nil || rmeta["purge_at"] != nil || rmeta["owner"] != f.adaID {
		t.Errorf("restore: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", bin+"/"+one, "", bearer(f.ada)); rec.Code != http.StatusOK {
		t.Errorf("get after restore: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", bin+"/"+one+"/restore", "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("restoring a live document: %d", rec.Code)
	}

	// Purge: its own rule (staff only), for deleted and live documents alike.
	f.doH(t, "DELETE", bin+"/"+one, "", bearer(f.ada))
	if rec, out := f.doH(t, "DELETE", bin+"/"+one+"?purge=true", "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("an owner purging: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", bin+"/"+one+"?purge=1", "", bearer(f.ada)); rec.Code != http.StatusBadRequest {
		t.Errorf("purge=1: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", posts+"/x?purge=true", "", bearer(f.ada)); rec.Code != http.StatusBadRequest {
		t.Errorf("purge on a collection that doesn't soft-delete: %d", rec.Code)
	}
	if err := f.svc.AddRole(ctx, "bob@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	if _, out := f.doH(t, "GET", bin+"?deleted=only", "", bearer(f.bob)); itemTitles(out) != "one" {
		t.Errorf("staff sees the whole trash: %v", out)
	}
	if rec, out := f.doH(t, "DELETE", bin+"/"+one+"?purge=true", "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Fatalf("staff purging: %d %v", rec.Code, out)
	}
	if _, out := f.doH(t, "GET", bin+"?deleted=include", "", bearer(f.ada)); strings.Contains(itemTitles(out), "one") {
		t.Errorf("purged document still there: %v", out)
	}
	two := ""
	_, out = f.doH(t, "GET", bin+"?order_by=title", "", bearer(f.ada))
	for _, it := range out["items"].([]any) {
		if it.(map[string]any)["title"] == "two" {
			two = it.(map[string]any)["id"].(string)
		}
	}
	if rec, _ := f.doH(t, "DELETE", bin+"/"+two+"?purge=true", "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("purging a live document: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", bin+"/"+two, "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("a purged live document: %d", rec.Code)
	}

	// Batch deletes are soft too, and API keys (outside the rules) see the trash.
	three := create(f.ada, "three")
	rec, out = f.doH(t, "POST", "/v1/acme/app/_batch", `{"operations":[{"op":"delete","collection":"bin","id":"`+three+`"}]}`, jsonAs(f.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch delete: %d %v", rec.Code, out)
	}
	if _, out := f.doH(t, "GET", bin+"?deleted=only", "", bearer(f.key)); itemTitles(out) != "three" {
		t.Errorf("key sees the trash with the batch's deletion: %v", out)
	}
	if rec, _ := f.doH(t, "POST", bin+"/"+three+"/restore", "", bearer(f.key)); rec.Code != http.StatusOK {
		t.Errorf("key restoring: %d", rec.Code)
	}
}

// Without a restore rule nobody but an API key sees the trash or restores.
func TestSoftDeleteRestoreIsDeniedWithoutItsRule(t *testing.T) {
	f := newRulesFixture(t, func(c *Config) {})
	_ = f // the bin collection of the fixture declares restore; "notes" doesn't soft-delete
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/notes?deleted=only", "", bearer(f.ada)); rec.Code != http.StatusBadRequest {
		t.Errorf("notes doesn't soft-delete: %d", rec.Code)
	}
}
