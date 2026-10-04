package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestIdempotentCreates(t *testing.T) {
	f := newRulesFixture(t)
	const batch = "/v1/acme/app/_batch"
	create := func(cred, key, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		h := map[string]string{"Content-Type": "application/json"}
		if cred != "" {
			h["Authorization"] = "Bearer " + cred
		}
		if key != "" {
			h["Idempotency-Key"] = key
		}
		rec, out := f.doH(t, "POST", posts, body, h)
		return rec, out
	}
	count := func() int {
		_, out := f.as(t, f.key, "GET", posts+"?limit=100", "")
		return len(out["items"].([]any))
	}
	const body = `{"title": "once", "author": {"email": "ada@example.com"}}`

	// Without a key, two requests are two documents.
	create(f.ada, "", body)
	create(f.ada, "", body)
	if count() != 2 {
		t.Fatalf("keyless creates: %d documents", count())
	}

	// With a key: the second request is the first one's answer.
	rec, first := create(f.ada, "order-1", body)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("first: %d %v", rec.Code, rec.Header())
	}
	rec, again := create(f.ada, "order-1", body)
	if rec.Code != http.StatusCreated || again["id"] != first["id"] || rec.Header().Get("Idempotent-Replayed") != "true" ||
		rec.Header().Get("Location") != posts+"/"+first["id"].(string) {
		t.Errorf("replay: %d %v %v", rec.Code, again, rec.Header())
	}
	if count() != 3 {
		t.Errorf("a replay created a document: %d", count())
	}
	// Same content, different spacing or key order: the same request.
	if rec, again = create(f.ada, "order-1", `{ "author": {"email": "ada@example.com"}, "title": "once" }`); again["id"] != first["id"] {
		t.Errorf("re-ordered body: %d %v", rec.Code, again)
	}

	// The original answer comes back even after the document changed.
	f.as(t, f.ada, "PATCH", posts+"/"+first["id"].(string), `{"title": "changed"}`)
	if _, again = create(f.ada, "order-1", body); again["title"] != "once" || again["_meta"].(map[string]any)["version"] != float64(1) {
		t.Errorf("replay after a change: %v", again)
	}

	// Another body with the same key is refused; another caller has a key space of their own.
	if rec, out := create(f.ada, "order-1", `{"title": "other", "author": {"email": "ada@example.com"}}`); rec.Code != http.StatusUnprocessableEntity || out["error"].(map[string]any)["code"] != "idempotency_key_reused" {
		t.Errorf("another body: %d %v", rec.Code, out)
	}
	if rec, other := create(f.bob, "order-1", `{"title": "bobs", "author": {"email": "bob@example.com"}}`); rec.Code != http.StatusCreated || other["id"] == first["id"] {
		t.Errorf("bob with ada's key: %d %v", rec.Code, other)
	}
	if rec, other := create(f.key, "order-1", `{"title": "service"}`); rec.Code != http.StatusCreated || other["id"] == first["id"] {
		t.Errorf("an API key with the same key: %d %v", rec.Code, other)
	}

	// A request that fails remembers nothing: the corrected retry goes through.
	if rec, _ := create(f.ada, "retry-me", `{"author": {"email": "ada@example.com"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid: %d", rec.Code)
	}
	// A refusal by the rules isn't remembered either: again 403, never 422 or 409.
	for range 2 {
		if rec, _ := create(f.carl, "refused", body); rec.Code != http.StatusForbidden {
			t.Fatalf("refused by the rules: %d", rec.Code)
		}
	}
	if rec, _ := create(f.ada, "retry-me", body); rec.Code != http.StatusCreated {
		t.Errorf("the corrected retry: %d", rec.Code)
	}

	// A key whose first request is still running.
	sum := sha256.Sum256([]byte(`{"author":{"email":"ada@example.com"},"title":"once"}`))
	running := auth.IdempotencyRecord{
		ID: auth.IdempotencyID("doc:app", "posts", "user:"+f.adaID, "in-flight"), Function: "app/posts", CallerActor: "user:" + f.adaID,
		InputHash: hex.EncodeToString(sum[:]), Mode: "sync",
	}
	if _, claimed, err := f.svc.ClaimIdempotency(context.Background(), running); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if rec, out := create(f.ada, "in-flight", body); rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "request_in_progress" {
		t.Errorf("in flight: %d %v", rec.Code, out)
	}

	// What can't be remembered is refused, not ignored.
	if rec, _ := create("", "k", body); rec.Code != http.StatusBadRequest {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec, _ := create(f.ada, "has a space", body); rec.Code != http.StatusBadRequest {
		t.Errorf("bad key: %d", rec.Code)
	}

	// Batches: the whole answer is remembered.
	ops := `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "b1", "author": {"email": "ada@example.com"}}}, {"op": "create", "collection": "notes", "document": {"title": "b2"}}]}`
	before := count()
	send := func(key, b string) (*httptest.ResponseRecorder, map[string]any) {
		return f.doH(t, "POST", batch, b, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json", "Idempotency-Key": key})
	}
	rec, one := send("batch-1", ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	rec, two := send("batch-1", ops)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || toJSON(t, one) != toJSON(t, two) {
		t.Errorf("batch replay: %d %v\n%v\n%v", rec.Code, rec.Header(), one, two)
	}
	if count() != before+1 {
		t.Errorf("a batch replay created documents: %d, want %d", count(), before+1)
	}
	if rec, _ := send("batch-1", strings.Replace(ops, "b1", "b3", 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("another batch with the same key: %d", rec.Code)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A realm without authentication has nowhere to remember keys.
func TestIdempotencyKeyNeedsAuthEnabled(t *testing.T) {
	f := newFixture(t)
	rec, _ := f.doH(t, "POST", items, `{"name": "x"}`, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "authentication enabled") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
