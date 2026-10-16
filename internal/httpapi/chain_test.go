package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// Writes made by a trigger run carry `_meta.chain`; nothing else does, and a client can't
// set it.
func TestWritesOfATriggerRunAreStampedWithTheChain(t *testing.T) {
	f := newRulesFixture(t)
	do := func(method, path, body, token string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if method == "PATCH" {
			req.Header.Set("Content-Type", "application/merge-patch+json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	token := func(c auth.CallbackClaims) string {
		c.Realm, c.Function, c.Expires = "acme", "app/echo", f.clock.Add(time.Minute)
		return auth.SignCallback(f.callbackKey, c)
	}
	chained := token(auth.CallbackClaims{Admin: true, ChainRoot: "evt-1", ChainDepth: 2})
	plain := token(auth.CallbackClaims{Admin: true})
	asAda := token(auth.CallbackClaims{UserID: f.adaID, ChainRoot: "evt-9", ChainDepth: 0})

	chainOf := func(out map[string]any) map[string]any {
		meta, _ := out["_meta"].(map[string]any)
		c, _ := meta["chain"].(map[string]any)
		return c
	}

	// Created by a trigger run acting as the function: stamped.
	code, doc := do("POST", "/v1/acme/app/private", `{"title": "from a trigger"}`, chained)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	if c := chainOf(doc); c["root"] != "evt-1" || c["depth"] != float64(2) {
		t.Fatalf("chain = %v, want root evt-1 depth 2", c)
	}
	id := doc["id"].(string)

	// Acting as a user keeps the user's ownership and rules, and is stamped too.
	code, note := do("POST", "/v1/acme/app/notes", `{"title": "as ada"}`, asAda)
	if code != 201 || chainOf(note)["root"] != "evt-9" || chainOf(note)["depth"] != float64(0) || note["_meta"].(map[string]any)["owner"] != f.adaID {
		t.Fatalf("as a user: %d %v", code, note)
	}

	// An update by something no trigger caused replaces the whole _meta: the chain is gone.
	code, doc = do("PATCH", "/v1/acme/app/private/"+id, `{"title": "edited"}`, plain)
	if code != 200 || chainOf(doc) != nil {
		t.Fatalf("update without a chain: %d %v", code, doc)
	}
	// ...and a trigger run's update stamps it again.
	code, doc = do("PATCH", "/v1/acme/app/private/"+id, `{"title": "again"}`, chained)
	if code != 200 || chainOf(doc)["root"] != "evt-1" {
		t.Fatalf("update with a chain: %d %v", code, doc)
	}

	// A client can't set it: `_meta` in a body is ignored.
	code, doc = do("POST", "/v1/acme/app/private", `{"title": "forged", "_meta": {"chain": {"root": "forged", "depth": 0}}}`, plain)
	if code != 201 || chainOf(doc) != nil {
		t.Fatalf("a client set the chain: %d %v", code, doc)
	}
}

// A function a trigger run calls with ctx.call carries the chain on, so what it writes is
// stamped too and the chain can't be shortened by hiding behind another function.
func TestChainCarriesThroughCtxCall(t *testing.T) {
	f := newRulesFixture(t)
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{
		Realm: "acme", Function: "app/checkout", Calls: []string{"reserve"}, Inv: "inv-1", UserID: f.adaID,
		ChainRoot: "evt-7", ChainDepth: 4, Expires: f.clock.Add(time.Minute),
	})
	req := httptest.NewRequest("POST", "/v1/acme/app/_func/reserve", strings.NewReader(`{"n": 1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("call: %d %s", rec.Code, rec.Body.String())
	}
	inner := f.runner.last().Envelope.Callback
	for name, token := range map[string]string{"token": inner.Token} {
		claims, err := auth.VerifyCallback(f.callbackKey, token, *f.clock)
		if err != nil || claims.ChainRoot != "evt-7" || claims.ChainDepth != 4 || claims.Depth != 1 {
			t.Errorf("%s of the callee: %+v %v", name, claims, err)
		}
	}
}

// A client can't plant a chain by any route that writes documents: not in the body of a
// create, a replace or a patch, and not through a batch. A forged chain would let a loop
// look first-party (depth reset) or make an event look chained (and be dropped).
func TestNoRouteLetsAClientSetTheChain(t *testing.T) {
	f := newRulesFixture(t)
	token := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", Admin: true, Expires: f.clock.Add(time.Minute)})
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if method == "PATCH" {
			req.Header.Set("Content-Type", "application/merge-patch+json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	chainIn := func(doc map[string]any) any {
		meta, _ := doc["_meta"].(map[string]any)
		return meta["chain"]
	}
	forged := `"_meta": {"chain": {"root": "forged", "depth": 0}}`

	code, doc := do("POST", "/v1/acme/app/private", `{"title": "a"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	id := doc["id"].(string)
	for name, tc := range map[string]struct{ method, path, body string }{
		"replace":    {"PUT", "/v1/acme/app/private/" + id, `{"title": "b", ` + forged + `}`},
		"patch":      {"PATCH", "/v1/acme/app/private/" + id, `{"title": "c", ` + forged + `}`},
		"patch meta": {"PATCH", "/v1/acme/app/private/" + id, `{"_meta": {"chain": {"root": "forged", "depth": 0}}}`},
		"create":     {"POST", "/v1/acme/app/private", `{"title": "d", ` + forged + `}`},
	} {
		code, out := do(tc.method, tc.path, tc.body)
		if code >= 500 {
			t.Errorf("%s: %d %v", name, code, out)
			continue
		}
		if code < 300 && chainIn(out) != nil {
			t.Errorf("%s: the chain was set: %v", name, chainIn(out))
		}
	}
	// Stored too, not only in the answer.
	code, stored := do("GET", "/v1/acme/app/private/"+id, "")
	if code != 200 || chainIn(stored) != nil {
		t.Errorf("stored document: %d chain %v", code, chainIn(stored))
	}
	// A batch.
	code, out := do("POST", "/v1/acme/app/_batch", `{"operations": [
		{"op": "create", "collection": "private", "document": {"title": "e", `+forged+`}},
		{"op": "patch", "collection": "private", "id": "`+id+`", "patch": {`+forged+`}}
	]}`)
	if code >= 500 {
		t.Fatalf("batch: %d %v", code, out)
	}
	for _, r := range out["results"].([]any) {
		if m, ok := r.(map[string]any); ok {
			if doc, ok := m["document"].(map[string]any); ok && chainIn(doc) != nil {
				t.Errorf("batch: the chain was set: %v", chainIn(doc))
			}
		}
	}
}
