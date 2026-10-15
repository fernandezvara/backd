package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/storage"
)

func TestAggregateEndpoint(t *testing.T) {
	f := newRulesFixture(t)
	sales := "/v1/acme/app/sales"
	create := func(token, body string) {
		t.Helper()
		if rec, out := f.doH(t, "POST", sales, body, jsonHdr(token)); rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", body, rec.Code, out)
		}
	}
	// ada's: 3 shoes (two paid around the end of September in Madrid), 1 hat. bob's: 1 shoe.
	create(f.ada, `{"category": "shoes", "total": 100.1, "paid_at": "2026-09-30T22:30:00Z"}`)
	create(f.ada, `{"category": "shoes", "total": 200.4, "paid_at": "2026-09-30T21:30:00Z"}`)
	create(f.ada, `{"category": "shoes", "total": 5}`)
	create(f.ada, `{"category": "hats", "total": 20, "paid_at": "2026-10-02T10:00:00Z"}`)
	create(f.bob, `{"category": "shoes", "total": 1000}`)

	get := func(token, q string) (int, map[string]any, http.Header) {
		t.Helper()
		var hdr map[string]string
		if token != "" {
			hdr = bearer(token)
		}
		rec, out := f.doH(t, "GET", sales+"/_aggregate"+q, "", hdr)
		return rec.Code, out, rec.Header()
	}
	groups := func(out map[string]any) []map[string]any {
		var gs []map[string]any
		for _, g := range out["groups"].([]any) {
			gs = append(gs, g.(map[string]any))
		}
		return gs
	}

	// Rules decide what is counted: ada's totals never include bob's documents.
	code, out, h := get(f.ada, "?group_by=category&metrics=count,sum:total,avg:total,max:paid_at&order_by=-count")
	gs := groups(out)
	if code != 200 || len(gs) != 2 || out["total_groups"] != float64(2) {
		t.Fatalf("by category: %d %v", code, out)
	}
	shoes := gs[0]
	if shoes["key"].(map[string]any)["category"] != "shoes" || shoes["count"] != float64(3) || shoes["sum_total"] != "305.5" || shoes["avg_total"] != "101.8333333333333333333333333333333" || shoes["max_paid_at"] != "2026-09-30T22:30:00.000Z" {
		t.Errorf("shoes = %v", shoes)
	}
	if h.Get("ETag") == "" || !strings.HasPrefix(h.Get("ETag"), `W/"`) || h.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("headers = %v", h)
	}
	// An administrator's key bypasses rules: everything is counted.
	if _, out, _ := get(f.key, "?metrics=count"); groups(out)[0]["count"] != float64(5) {
		t.Errorf("key total = %v", out)
	}
	// No group_by is one total.
	if _, out, _ := get(f.ada, ""); len(groups(out)) != 1 || groups(out)[0]["count"] != float64(4) || len(groups(out)[0]["key"].(map[string]any)) != 0 {
		t.Errorf("total = %v", out)
	}
	// Buckets in the business's time zone: 22:30Z on Sep 30th is already October in Madrid,
	// 21:30Z is not.
	_, out, _ = get(f.ada, "?group_by=paid_at:month&tz=Europe/Madrid&order_by=paid_at:month")
	gs = groups(out)
	keys := []any{}
	for _, g := range gs {
		keys = append(keys, g["key"].(map[string]any)["paid_at:month"])
	}
	if fmt.Sprint(keys) != "[<nil> 2026-09 2026-10]" || gs[2]["count"] != float64(2) {
		t.Errorf("months in Madrid = %v %v", keys, gs)
	}
	_, out, _ = get(f.ada, "?group_by=paid_at:month&order_by=paid_at:month")
	if len(groups(out)) != 3 || groups(out)[1]["count"] != float64(2) {
		t.Errorf("months in UTC = %v", out)
	}
	// where narrows before grouping, and ?org=... is validated like a list's.
	_, out, _ = get(f.ada, `?where={"category":"shoes"}&metrics=count`)
	if groups(out)[0]["count"] != float64(3) {
		t.Errorf("where = %v", out)
	}
	// limit and total_groups
	_, out, _ = get(f.ada, "?group_by=category,paid_at:day&limit=1")
	if len(groups(out)) != 1 || out["total_groups"] != float64(3) {
		t.Errorf("limit = %v", out)
	}

	// Conditional GET.
	rec, _ := f.doH(t, "GET", sales+"/_aggregate?group_by=category&metrics=count,sum:total,avg:total,max:paid_at&order_by=-count", "", map[string]string{"Authorization": "Bearer " + f.ada, "If-None-Match": h.Get("ETag")})
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d", rec.Code)
	}

	// Errors.
	for q, want := range map[string]string{
		"?group_by=total":                                         "not a field this collection declares in aggregate.group_by",
		"?group_by=category:month":                                "only dates can be bucketed",
		"?group_by=paid_at:fortnight":                             "is not a bucket",
		"?group_by=category,category":                             "is listed twice",
		"?group_by=category,paid_at,_meta.owner,_meta.created_at": "at most 3",
		"?metrics=sum:category":                                   "needs a field of aggregate.metrics",
		"?metrics=sum:paid_at":                                    "only numbers can be summed",
		"?metrics=median:total":                                   "is not a metric",
		"?metrics=count_distinct:total":                           "needs a field of aggregate.group_by",
		"?order_by=-sum_total":                                    "is not in this request's group_by or metrics",
		"?limit=51":                                               "between 1 and 50",
		"?tz=Mars/Base":                                           "is not an IANA time zone",
		`?where={"nope":1}`:                                       "unknown field",
		"?group_by=category&frob=1":                               "unknown query parameter",
	} {
		code, out, _ := get(f.ada, q)
		if code != 400 || errCode(out) != "invalid_aggregate" || !strings.Contains(fmt.Sprint(out), want) {
			t.Errorf("%s: %d %v, want %q", q, code, out, want)
		}
	}
	// Not declared, not signed in, no rule.
	if code, out, _ := f.doRaw(t, "/v1/acme/app/posts/_aggregate", f.ada); code != 400 || errCode(out) != "aggregate_not_enabled" {
		t.Errorf("not enabled: %d %v", code, out)
	}
	if code, out, _ := get("", ""); code != 401 || errCode(out) != "unauthenticated" {
		t.Errorf("anonymous: %d %v", code, out)
	}
	if code, out, _ := f.doRaw(t, "/v1/acme/app/private/_aggregate", f.ada); code != 403 {
		t.Errorf("no read rule: %d %v", code, out)
	}

	// Too expensive: the database says so.
	f.store.aggregateErr = storage.ErrAggregateTooExpensive
	code, out, _ = get(f.ada, "?group_by=category")
	f.store.aggregateErr = nil
	if code != 422 || errCode(out) != "aggregate_too_expensive" {
		t.Errorf("too expensive: %d %v", code, out)
	}
	// What was asked of the database: the collection's own limits, never above the instance's.
	if q := f.store.lastAggregate; q.MaxDistinct != 10000 || q.MaxTime <= 0 {
		t.Errorf("limits = %+v", q)
	}
	// One at a time per caller and collection.
	key := "aggregate/acme/app/sales/user:" + f.adaID
	l := limiterFor(f.reg)
	if !l.tryAcquire(key, 1) {
		t.Fatal("limiter was busy")
	}
	code, out, h = get(f.ada, "")
	l.release(key, 1)
	if code != 429 || h.Get("Retry-After") != "1" || errCode(out) != "too_many_requests" {
		t.Errorf("concurrent: %d %v %v", code, out, h)
	}
}

// doRaw is a GET with a session token, for the paths of other collections.
func (f *rulesFixture) doRaw(t *testing.T, path, token string) (int, map[string]any, http.Header) {
	t.Helper()
	rec, out := f.doH(t, "GET", path, "", bearer(token))
	return rec.Code, out, rec.Header()
}
