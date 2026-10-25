package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The review of reference expansion (design/reference-expansion.md §12): what a caller can learn
// through `expand` is what a direct read would tell them, and nothing about what they can't read.
func TestExpansionSecurity(t *testing.T) {
	f := newRulesFixture(t)
	news := "/v1/acme/app/news"
	create := func(token, path, body string) string {
		t.Helper()
		rec, out := f.doH(t, "POST", path, body, jsonHdr(token))
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %s: %d %v", path, rec.Code, out)
		}
		return out["id"].(string)
	}
	read := func(token, path string) (*httpRecorderLike, map[string]any) {
		t.Helper()
		var hdr map[string]string
		if token != "" {
			hdr = bearer(token)
		}
		rec, out := f.doH(t, "GET", path, "", hdr)
		return &httpRecorderLike{Code: rec.Code, Header: rec.Header(), Body: rec.Body.String()}, out
	}
	expanded := func(out map[string]any, name string) any {
		t.Helper()
		e, _ := out["_expanded"].(map[string]any)
		if e == nil {
			t.Fatalf("no _expanded in %v", out)
		}
		v, present := e[name]
		if !present {
			t.Fatalf("%s is not in %v: an unavailable reference must be present, as null", name, e)
		}
		return v
	}

	// 1. Missing, unreadable and of another organization are the same answer, byte for byte.
	orgA := f.orgOf(t, f.ada, "Alpha")
	project := create(f.ada, "/v1/acme/app/projects?org="+orgA, `{"title": "Secret plan"}`)
	hidden := create(f.ada, news, `{"title": "t1", "published": true, "status": "`+project+`"}`)
	absent := create(f.ada, news, `{"title": "t2", "published": true, "status": "noproject0000000000000"}`)
	// A document of an organization the reader isn't in, for the same news id's reference.
	rec, bodyHidden := read(f.bob, news+"/"+hidden+"?expand=related")
	rec2, bodyAbsent := read(f.bob, news+"/"+absent+"?expand=related")
	if rec.Code != 200 || rec2.Code != 200 || expanded(bodyHidden, "related") != nil || expanded(bodyAbsent, "related") != nil {
		t.Fatalf("bob: %d %v / %d %v", rec.Code, bodyHidden, rec2.Code, bodyAbsent)
	}
	for _, h := range []string{"X-Backd-Expand-Queries", "X-Backd-Expand-Docs", "Cache-Control", "Content-Type"} {
		if rec.Header.Get(h) != rec2.Header.Get(h) {
			t.Errorf("%s differs between unreadable (%q) and missing (%q)", h, rec.Header.Get(h), rec2.Header.Get(h))
		}
	}
	// Normalized by what legitimately differs (ids, timestamps), the bodies are the same.
	strip := func(b string, id string) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(b), &m)
		delete(m, "id")
		delete(m, "_meta")
		delete(m, "title")
		delete(m, "status")
		out, _ := json.Marshal(m)
		return strings.ReplaceAll(string(out), id, "")
	}
	if strip(rec.Body, hidden) != strip(rec2.Body, absent) {
		t.Errorf("bodies differ:\n%s\n%s", rec.Body, rec2.Body)
	}
	// The member of the organization does get it.
	if _, out := read(f.sessionIn(t, f.ada, orgA), news+"/"+hidden+"?expand=related(title)"); expanded(out, "related") == nil {
		t.Errorf("a member must read her own organization's project: %v", out)
	}

	// 2. A soft-deleted target is gone for expansion as it is for a read.
	trashed := create(f.ada, "/v1/acme/app/bin", `{"title": "b"}`)
	linked := create(f.ada, news, `{"title": "`+trashed+`", "published": true}`)
	if _, out := read(f.ada, news+"/"+linked+"?expand=trashed"); expanded(out, "trashed") == nil {
		t.Fatalf("live target: %v", out)
	}
	if rec, _ := f.doH(t, "DELETE", "/v1/acme/app/bin/"+trashed, "", bearer(f.ada)); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, out := read(f.ada, news+"/"+linked+"?expand=trashed"); expanded(out, "trashed") != nil {
		t.Errorf("a soft-deleted target expanded: %v", out)
	}

	// 3. The selection is applied after the rule, and never shows more than a read would.
	visible := create(f.ada, "/v1/acme/app/posts", `{"title": "Public", "published": true, "members": ["someone"]}`)
	draft := create(f.ada, "/v1/acme/app/posts", `{"title": "Draft", "published": false, "members": ["someone"]}`)
	for _, c := range []struct {
		target string
		want   bool
	}{{visible, true}, {draft, false}} {
		id := create(f.ada, news, `{"title": "p", "published": true, "status": "`+c.target+`"}`)
		_, out := read(f.bob, news+"/"+id+"?expand=post(title)")
		got := expanded(out, "post")
		if c.want {
			m, _ := got.(map[string]any)
			if m["title"] == nil || m["members"] != nil || m["published"] != nil || m["_meta"] != nil || len(m) != 2 {
				t.Errorf("selected fields leaked more: %v", got)
			}
		} else if got != nil {
			t.Errorf("an unreadable draft expanded: %v", got)
		}
	}

	// 4. An anonymous reader of a publicly cached collection never gets a public expanded response.
	for _, p := range []string{news + "/" + linked + "?expand=trashed", news + "?expand=related,post"} {
		rec, _ := read("", p)
		if rec.Code != 200 || rec.Header.Get("Cache-Control") != "private, no-cache" {
			t.Errorf("anonymous %s: %d %q", p, rec.Code, rec.Header.Get("Cache-Control"))
		}
		if strings.Contains(rec.Header.Get("Cache-Control"), "public") {
			t.Errorf("an expanded response was marked public")
		}
	}
	// ...and without expand the same read is still public (the baseline this guards).
	if rec, _ := read("", news); rec.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Errorf("baseline public read: %q", rec.Header.Get("Cache-Control"))
	}
	// An anonymous reader gets nothing of what needs a sign-in: bin needs a user.
	if _, out := read("", news+"/"+linked+"?expand=trashed"); out["_expanded"] != nil && expanded(out, "trashed") != nil {
		t.Errorf("anonymous expanded a target that needs a sign-in: %v", out)
	}

	// 5. Never past the declared relations: a name that isn't one is a 400, whatever it looks like.
	for _, e := range []string{"posts", "_meta", "bin", "related.related.related", "../x", "related(title)(x)"} {
		rec, out := f.doH(t, "GET", news+"/"+linked+"?expand="+e, "", bearer(f.ada))
		if rec.Code != 400 || errCode(out) != "invalid_expand" {
			t.Errorf("expand=%s: %d %v", e, rec.Code, out)
		}
	}
}

// httpRecorderLike is what the review test keeps of a response.
type httpRecorderLike struct {
	Code   int
	Header http.Header
	Body   string
}
