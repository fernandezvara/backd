package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// Public caching (design/conditional-get.md §5): only anonymous reads of the
// default view, in a collection that opted in, may be stored by shared caches.
func TestPublicCaching(t *testing.T) {
	f := newRulesFixture(t)
	const news = "/v1/acme/app/news"
	cc := func(h http.Header) string { return h.Get("Cache-Control") }

	_, pub := f.doH(t, "POST", news, `{"title": "hello", "published": true}`, bearer(f.ada))
	_, draft := f.doH(t, "POST", news, `{"title": "draft", "published": false}`, bearer(f.ada))
	pubDoc := news + "/" + pub["id"].(string)

	// Anonymous reads of the default view are public, with a validator.
	for _, path := range []string{pubDoc, news} {
		rec, out := f.doH(t, "GET", path, "", nil)
		if rec.Code != http.StatusOK || cc(rec.Header()) != "public, max-age=60" || rec.Header().Get("ETag") == "" ||
			!strings.Contains(rec.Header().Get("Vary"), "Authorization") || !strings.Contains(rec.Header().Get("Vary"), "Cookie") {
			t.Errorf("anonymous GET %s = %d %v", path, rec.Code, rec.Header())
		}
		// The anonymous list holds only what anonymous callers may read.
		if items, ok := out["items"].([]any); ok && len(items) != 1 {
			t.Errorf("anonymous list has %d items: %v", len(items), items)
		}
		// And it revalidates: 304 keeps the public header.
		rec2, _ := f.doH(t, "GET", path, "", map[string]string{"If-None-Match": rec.Header().Get("ETag")})
		if rec2.Code != http.StatusNotModified || cc(rec2.Header()) != "public, max-age=60" {
			t.Errorf("anonymous revalidation %s = %d %v", path, rec2.Code, rec2.Header())
		}
	}

	// Anything with credentials stays private, even for the same document.
	for name, hdr := range map[string]map[string]string{
		"a session":  bearer(f.ada),
		"an API key": bearer(f.key),
		"a cookie":   {"Cookie": "other=1"},
	} {
		rec, _ := f.doH(t, "GET", pubDoc, "", hdr)
		if cc(rec.Header()) != "private, no-cache" {
			t.Errorf("%s: Cache-Control = %q", name, cc(rec.Header()))
		}
		rec, _ = f.doH(t, "GET", news, "", hdr)
		if cc(rec.Header()) != "private, no-cache" {
			t.Errorf("list with %s: Cache-Control = %q", name, cc(rec.Header()))
		}
	}

	// A document anonymous callers can't read is not public: 401/404, no public header.
	rec, _ := f.doH(t, "GET", news+"/"+draft["id"].(string), "", nil)
	if cc(rec.Header()) == "public, max-age=60" || rec.Code == http.StatusOK {
		t.Errorf("draft as anonymous = %d %v", rec.Code, rec.Header())
	}

	// Never public: the trash view, and a response with file links.
	f.doH(t, "DELETE", pubDoc, "", bearer(f.ada))
	rec, _ = f.doH(t, "GET", news+"?deleted=include", "", nil)
	if cc(rec.Header()) == "public, max-age=60" {
		t.Errorf("trash view is public: %v", rec.Header())
	}

	// A collection that didn't opt in is private whoever asks.
	rec, _ = f.doH(t, "GET", "/v1/acme/app/posts", "", nil)
	if cc(rec.Header()) != "private, no-cache" {
		t.Errorf("collection without cache: %q", cc(rec.Header()))
	}
}
