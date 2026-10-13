package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestRepresentationTag(t *testing.T) {
	if got := representationTag(nil); got != "" {
		t.Errorf("full document tag = %q", got)
	}
	a := representationTag([]string{"salary", "email"})
	if b := representationTag([]string{"email", "salary"}); a != b {
		t.Errorf("order changes the tag: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, ".r") || len(a) != 10 {
		t.Errorf("tag = %q", a)
	}
	if a == representationTag([]string{"email"}) {
		t.Error("different views share a tag")
	}
	if got := docETag(12, a); got != `"12`+a+`"` {
		t.Errorf("docETag = %s", got)
	}
}

func TestNoneMatch(t *testing.T) {
	for _, tc := range []struct {
		header []string
		want   bool
	}{
		{nil, false},
		{[]string{`"3"`}, true},
		{[]string{`W/"3"`}, true},
		{[]string{`"1", "3"`}, true},
		{[]string{`"1"`, `"3"`}, true},
		{[]string{`*`}, true},
		{[]string{`"4"`}, false},
		{[]string{`3`}, false},
	} {
		if got := noneMatch(tc.header, `"3"`); got != tc.want {
			t.Errorf("noneMatch(%q) = %v", tc.header, got)
		}
	}
}

func TestConditionalGetDocument(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", items, `{"name": "a"}`)
	doc := items + "/" + created["id"].(string)

	rec, _ := f.do(t, "GET", doc, "")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("GET = %d %v", rec.Code, rec.Header())
	}

	rec, _ = f.doH(t, "GET", doc, "", map[string]string{"If-None-Match": `"1"`})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != `"1"` ||
		rec.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(rec.Header().Get("Vary"), "Authorization") {
		t.Errorf("matching If-None-Match = %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	for _, v := range []string{`W/"1"`, `"0", "1"`, `*`} {
		if rec, _ = f.doH(t, "GET", doc, "", map[string]string{"If-None-Match": v}); rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s = %d", v, rec.Code)
		}
	}

	// A change moves the tag: the old one now gets the document.
	f.do(t, "PATCH", doc, `{"age": 3}`)
	rec, _ = f.doH(t, "GET", doc, "", map[string]string{"If-None-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || rec.Body.Len() == 0 {
		t.Errorf("stale If-None-Match = %d %v", rec.Code, rec.Header())
	}

	// A missing document is 404, whatever the validator says.
	if rec, _ = f.doH(t, "GET", items+"/nope", "", map[string]string{"If-None-Match": `*`}); rec.Code != http.StatusNotFound {
		t.Errorf("missing document with If-None-Match * = %d", rec.Code)
	}

	// A tag with a representation suffix still guards the write.
	rec, _ = f.doH(t, "PATCH", doc, `{"age": 4}`, map[string]string{"If-Match": `"2.r1a2b3c4d"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("suffixed If-Match = %d %v", rec.Code, rec.Header())
	}
}
