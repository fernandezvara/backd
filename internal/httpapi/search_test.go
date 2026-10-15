package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/storage"
)

func TestSearch(t *testing.T) {
	f := newRulesFixture(t)
	articles := "/v1/acme/app/articles"
	for _, a := range []string{
		`{"title": "Running shoes", "body": "Light shoes", "lang": "en", "status": "published"}`,
		`{"title": "Kids shoes", "body": "running for kids", "lang": "en", "status": "published"}`,
		`{"title": "Boots", "body": "Heavy boots", "lang": "en", "status": "draft"}`,
	} {
		if rec, out := f.doH(t, "POST", articles, a, jsonHdr(f.ada)); rec.Code != 201 {
			t.Fatalf("create: %d %v", rec.Code, out)
		}
	}
	get := func(token, q string, hdr ...string) (int, map[string]any, http.Header) {
		t.Helper()
		h := map[string]string{}
		if token != "" {
			h["Authorization"] = "Bearer " + token
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			h[hdr[i]] = hdr[i+1]
		}
		rec, out := f.doH(t, "GET", articles+q, "", h)
		return rec.Code, out, rec.Header()
	}
	where := func(w string) string { return "?where=" + url.QueryEscape(w) }
	items := func(out map[string]any) []map[string]any {
		var r []map[string]any
		for _, i := range out["items"].([]any) {
			r = append(r, i.(map[string]any))
		}
		return r
	}

	// A search ANDed with other conditions, sorted by relevance with the score in _meta.
	code, out, _ := get(f.ada, where(`{"$search": "running", "status": "published"}`)+"&order_by=_relevance")
	got := items(out)
	if code != 200 || len(got) != 2 {
		t.Fatalf("search: %d %v", code, out)
	}
	if got[0]["title"] != "Running shoes" || got[0]["_meta"].(map[string]any)["score"].(float64) < got[1]["_meta"].(map[string]any)["score"].(float64) {
		t.Errorf("order = %v", got)
	}
	if out["next_cursor"] != nil {
		t.Errorf("relevance pages by skip, no cursor: %v", out)
	}
	// The search is the query's own, in the user's language (ada's locale is en).
	if q := f.store.lastQuery; q.Search == nil || q.Search.Text != "running" || q.Search.Language != "english" {
		t.Errorf("query = %+v", q.Search)
	}

	// Language: language=, then the user's locale, then Accept-Language for anonymous callers.
	get(f.ada, where(`{"$search": "x"}`)+"&language=es-MX")
	if f.store.lastQuery.Search.Language != "spanish" {
		t.Errorf("language param = %q", f.store.lastQuery.Search.Language)
	}
	get(f.ada, where(`{"$search": "x"}`)+"&language=xx")
	if f.store.lastQuery.Search.Language != "none" {
		t.Errorf("unknown language = %q", f.store.lastQuery.Search.Language)
	}
	f.doH(t, "GET", articles+where(`{"$search": "x"}`), "", map[string]string{"Authorization": "Bearer " + f.key, "Accept-Language": "fr-CA,fr;q=0.9"})
	if f.store.lastQuery.Search.Language != "french" {
		t.Errorf("Accept-Language = %q", f.store.lastQuery.Search.Language)
	}

	// Offsets stop at 1,000 results.
	if code, out, _ := get(f.ada, where(`{"$search": "running"}`)+"&order_by=_relevance&skip=990&limit=20"); code != 400 || !strings.Contains(fmt.Sprint(out), "first 1000 results") {
		t.Errorf("deep page: %d %v", code, out)
	}
	if code, _, _ := get(f.ada, where(`{"$search": "running"}`)+"&order_by=_relevance&skip=900&limit=100"); code != 200 {
		t.Errorf("page 10: %d", code)
	}

	// Errors.
	for q, want := range map[string]string{
		where(`{"$or": [{"$search": "a"}, {"status": "x"}]}`):        "can't be inside $or or $and",
		where(`{"$search": 5}`):                                      "non-empty string",
		where(`{"status": "published"}`) + "&order_by=_relevance":    "_relevance needs a $search",
		where(`{"$search": "a"}`) + "&order_by=_relevance,title":     "can't be combined with other fields",
		where(`{"$search": "a"}`) + "&order_by=_relevance&after=abc": "can't be combined with order_by=_relevance",
		where(`{"$search": "` + strings.Repeat("w ", 21) + `"}`):     "at most 20",
	} {
		code, out, _ := get(f.ada, q)
		if code != 400 || errCode(out) != "invalid_query" || !strings.Contains(fmt.Sprint(out), want) {
			t.Errorf("%s: %d %v, want %q", q, code, out, want)
		}
	}
	// A collection without a text index, and a rebuild in progress.
	if rec, out := f.doH(t, "GET", "/v1/acme/app/notes"+where(`{"$search": "go"}`), "", bearer(f.ada)); rec.Code != 400 || errCode(out) != "search_not_enabled" {
		t.Errorf("no text index: %d %v", rec.Code, out)
	}
	f.store.fail = storage.ErrIndexBuilding
	code, out, h := get(f.ada, where(`{"$search": "go"}`))
	f.store.fail = nil
	if code != 503 || errCode(out) != "index_building" || h.Get("Retry-After") != "5" {
		t.Errorf("rebuild: %d %v %v", code, out, h)
	}

	// $search in an aggregation: results per language beside a search box.
	rec, agg := f.doH(t, "GET", articles+"/_aggregate"+where(`{"$search": "shoes"}`)+"&group_by=status", "", bearer(f.ada))
	if rec.Code != 200 || agg["total_groups"] != float64(1) {
		t.Errorf("aggregate with search: %d %v", rec.Code, agg)
	}
	rec, agg = f.doH(t, "GET", "/v1/acme/app/sales/_aggregate"+where(`{"$search": "x"}`), "", bearer(f.ada))
	if rec.Code != 400 || errCode(agg) != "search_not_enabled" {
		t.Errorf("aggregate without text index: %d %v", rec.Code, agg)
	}
}
