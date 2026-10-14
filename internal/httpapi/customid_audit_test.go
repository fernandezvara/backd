package httpapi

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

// Ids that look like the words of the routes under a collection are ordinary documents: the
// routes that start with an underscore are the only reserved ones, and an id can't start with
// one.
func TestCustomIDsNextToReservedSegments(t *testing.T) {
	f := newRulesFixture(t)
	for _, id := range []string{"restore", "files", "uploads", "complete", "count", "new", "a:b", "x@y.z", "t~1", "1.2"} {
		path := "/v1/acme/app/slugs/" + url.PathEscape(id)
		if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/slugs", `{"id": "`+id+`", "title": "t"}`); code != http.StatusCreated {
			t.Fatalf("create %q: %d %v", id, code, out)
		}
		if code, out := f.as(t, f.ada, "GET", path, ""); code != 200 || out["id"] != id {
			t.Errorf("get %q: %d %v", id, code, out)
		}
		if code, _ := f.as(t, f.ada, "PATCH", path, `{"title": "u"}`); code != 200 {
			t.Errorf("patch %q: %d", id, code)
		}
		if code, _ := f.as(t, f.ada, "DELETE", path, ""); code != 204 {
			t.Errorf("delete %q: %d", id, code)
		}
		if code, out := f.as(t, f.ada, "POST", path+"/restore", ""); code != 200 || out["id"] != id {
			t.Errorf("restore %q: %d %v", id, code, out)
		}
	}
	// The reserved segments themselves can't be ids.
	for _, id := range []string{"_files", "_aggregate", "_batch", "_transfer", ".well-known", "a..b", "a/b", "a%2Fb"} {
		if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/slugs", `{"id": "`+id+`", "title": "t"}`); code != http.StatusBadRequest || errCode(out) != "invalid_id" {
			t.Errorf("create %q: %d %v, want 400 invalid_id", id, code, out)
		}
	}
}

// Lists without order_by follow creation time, then id: ids chosen by clients have no order of
// their own, and the cursor walks it.
func TestListOrderOfCustomIDs(t *testing.T) {
	f := newRulesFixture(t)
	created := []string{"zeta", "alpha", "mu", "beta"}
	for _, id := range created {
		if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/slugs", `{"id": "`+id+`", "title": "t"}`); code != 201 {
			t.Fatalf("create %q: %d %v", id, code, out)
		}
		*f.clock = f.clock.Add(time.Second)
	}
	idsOf := func(out map[string]any) []string {
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		return got
	}
	_, out := f.as(t, f.ada, "GET", "/v1/acme/app/slugs", "")
	if got := idsOf(out); !reflect.DeepEqual(got, created) {
		t.Errorf("default order = %v, want creation order %v", got, created)
	}
	if _, out = f.as(t, f.ada, "GET", "/v1/acme/app/slugs?order_by=id", ""); !reflect.DeepEqual(idsOf(out), []string{"alpha", "beta", "mu", "zeta"}) {
		t.Errorf("order_by=id = %v", idsOf(out))
	}
	// Documents made in the same instant are told apart by id.
	same := []string{"s3", "s1", "s2"}
	for _, id := range same {
		f.as(t, f.ada, "POST", "/v1/acme/app/slugs", `{"id": "`+id+`", "title": "t"}`)
	}
	var walked []string
	next := ""
	for {
		path := "/v1/acme/app/slugs?limit=3"
		if next != "" {
			path += "&after=" + url.QueryEscape(next)
		}
		_, out = f.as(t, f.ada, "GET", path, "")
		walked = append(walked, idsOf(out)...)
		if out["has_more"] != true {
			break
		}
		next = out["next_cursor"].(string)
	}
	want := append(append([]string{}, created...), "s1", "s2", "s3")
	if !reflect.DeepEqual(walked, want) {
		t.Errorf("walking with cursors = %v, want %v", walked, want)
	}
}

// ETags and conditional requests don't depend on how the id was made.
func TestCustomIDsWithETagsAndConditionalGet(t *testing.T) {
	f := newRulesFixture(t)
	rec, _ := f.doH(t, "POST", "/v1/acme/app/slugs", `{"id": "INV:1@x", "title": "t"}`, bearer(f.ada))
	if rec.Code != 201 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	path := "/v1/acme/app/slugs/" + url.PathEscape("INV:1@x")
	rec, _ = f.doH(t, "GET", path, "", bearer(f.ada))
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || tag == "" {
		t.Fatalf("get: %d %q", rec.Code, tag)
	}
	h := bearer(f.ada)
	h["If-None-Match"] = tag
	if rec, _ = f.doH(t, "GET", path, "", h); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	h = bearer(f.ada)
	h["If-Match"] = `"7"`
	h["Content-Type"] = "application/merge-patch+json"
	if rec, _ = f.doH(t, "PATCH", path, `{"title": "u"}`, h); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("If-Match of another version: %d", rec.Code)
	}
}
