package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// The review of aggregations and search (design/query-language.md §7): what a caller can learn
// through them is what a list shows, and what a request can make the server run is bounded.
func TestAggregateAndSearchSecurity(t *testing.T) {
	f := newRulesFixture(t)
	sales, articles := "/v1/acme/app/sales", "/v1/acme/app/articles"
	create := func(token, path, body string) {
		t.Helper()
		if rec, out := f.doH(t, "POST", path, body, jsonHdr(token)); rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %v", rec.Code, out)
		}
	}
	create(f.ada, sales, `{"category": "secret-plans", "total": 1000, "paid_at": "2026-01-01T00:00:00Z"}`)
	create(f.ada, sales, `{"category": "secret-plans", "total": 500}`)
	create(f.bob, sales, `{"category": "hats", "total": 7}`)
	get := func(token, path string) (int, map[string]any) {
		t.Helper()
		var hdr map[string]string
		if token != "" {
			hdr = bearer(token)
		}
		rec, out := f.doH(t, "GET", path, "", hdr)
		return rec.Code, out
	}

	// 1. Bob's totals, groups, distinct counts and extremes cover only bob's documents; nothing
	// of ada's shows in a group key, a count of groups, or a minimum.
	code, out := get(f.bob, sales+"/_aggregate?group_by=category&metrics=count,sum:total,max:total,min:paid_at,count_distinct:_meta.owner")
	body := fmt.Sprint(out)
	if code != 200 || out["total_groups"] != float64(1) || strings.Contains(body, "secret-plans") || strings.Contains(body, "1000") {
		t.Errorf("bob sees: %d %v", code, out)
	}
	if code, out := get(f.bob, sales+"/_aggregate?group_by=_meta.owner"); code != 200 || out["total_groups"] != float64(1) {
		t.Errorf("owners: %d %v", code, out)
	}
	// A where that only ada's documents match counts nothing for bob: a total over nothing, not a hint.
	_, out = get(f.bob, sales+"/_aggregate?where="+url.QueryEscape(`{"category":"secret-plans"}`))
	if g := out["groups"].([]any); len(g) != 1 || g[0].(map[string]any)["count"] != float64(0) {
		t.Errorf("hidden where = %v", out)
	}

	// 2. Search never matches what a list hides, and an aggregate with a search neither.
	create(f.ada, articles, `{"title": "Zebra plan", "body": "secret"}`)
	for _, q := range []string{
		"?where=" + url.QueryEscape(`{"$search":"zebra"}`) + "&order_by=_relevance",
	} {
		code, out := get("", articles+q)
		if code != 401 {
			t.Errorf("anonymous search: %d %v", code, out)
		}
	}
	// articles' read rule is user != nil: every signed-in user may read it. The collection with a
	// per-owner rule is sales, which has no text index: the search is refused, not empty.
	if code, out := get(f.bob, sales+"?where="+url.QueryEscape(`{"$search":"secret"}`)); code != 400 || errCode(out) != "search_not_enabled" {
		t.Errorf("search without index: %d %v", code, out)
	}

	// 3. What a request can ask the server to run is bounded and validated: nothing is passed on.
	abusive := map[string]string{
		"four keys":         "?group_by=category,paid_at,_meta.owner,_meta.created_at",
		"eleven metrics":    "?metrics=" + strings.Repeat("count,", 10) + "sum:total",
		"operator as key":   "?group_by=" + url.QueryEscape(`$where`),
		"path as zone":      "?group_by=paid_at:day&tz=" + url.QueryEscape("../../etc/passwd"),
		"local zone":        "?group_by=paid_at:day&tz=Local",
		"long zone":         "?group_by=paid_at:day&tz=" + strings.Repeat("A", 5000),
		"offset zone":       "?group_by=paid_at:day&tz=%2B02:00",
		"negative limit":    "?limit=-1",
		"huge limit":        "?limit=1000000",
		"hidden field":      "?group_by=_search_lang",
		"id":                "?group_by=id",
		"version":           "?metrics=sum:_meta.version",
		"where operator":    "?where=" + url.QueryEscape(`{"$expr":{"$gt":["$a",1]}}`),
		"where function":    "?where=" + url.QueryEscape(`{"$where":"sleep(10000)"}`),
		"order by pipeline": "?order_by=" + url.QueryEscape(`{"$sort":1}`),
	}
	for name, q := range abusive {
		code, out := get(f.ada, sales+"/_aggregate"+q)
		if code != 400 || (errCode(out) != "invalid_aggregate" && errCode(out) != "invalid_query") {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// The stored language is server-owned: it can't be filtered, sorted or searched by.
	for _, q := range []string{
		"?where=" + url.QueryEscape(`{"_search_lang":"spanish"}`),
		"?order_by=_search_lang",
	} {
		if code, out := get(f.ada, articles+q); code != 400 || errCode(out) != "invalid_query" {
			t.Errorf("articles%s: %d %v", q, code, out)
		}
	}

	// 4. The reserved path is nothing else, and the admin data route has no aggregate.
	if rec, _ := f.doH(t, "DELETE", sales+"/_aggregate", "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE _aggregate = %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", sales+"/_aggregate", "{}", jsonHdr(f.ada)); rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
		t.Errorf("POST _aggregate = %d", rec.Code)
	}
	if code, _ := get(f.key, "/v1/acme/_admin/data/app/sales/_aggregate"); code != http.StatusNotFound {
		t.Errorf("admin data route aggregate = %d", code)
	}

	// 5. An API key's scopes apply: a key that may read another collection can't aggregate this one.
	scopes, err := auth.ParseScopes([]string{"read:app/notes"})
	if err != nil {
		t.Fatal(err)
	}
	_, narrow, err := f.svc.CreateAPIKey(context.Background(), "narrow-agg", auth.KeyOptions{Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	if code, out := get(narrow, sales+"/_aggregate"); code != http.StatusForbidden {
		t.Errorf("scoped key: %d %v", code, out)
	}
}
