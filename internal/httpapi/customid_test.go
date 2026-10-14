package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreateWithACustomID(t *testing.T) {
	f := newRulesFixture(t)
	post := func(cred, path, body string) (int, map[string]any) {
		return f.as(t, cred, "POST", path, body)
	}

	// A valid id is the document's id, and the answer says where it is.
	rec, out := f.doH(t, "POST", "/v1/acme/app/invoices", `{"id": "INV-0042", "title": "first"}`, bearer(f.ada))
	if rec.Code != http.StatusCreated || out["id"] != "INV-0042" {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/acme/app/invoices/INV-0042" {
		t.Errorf("Location = %q", loc)
	}
	if code, got := f.as(t, f.ada, "GET", "/v1/acme/app/invoices/INV-0042", ""); code != 200 || got["title"] != "first" {
		t.Errorf("get: %d %v", code, got)
	}

	// Never overwrites: a duplicate is 409 id_taken, and the document is untouched.
	code, out := post(f.bob, "/v1/acme/app/invoices", `{"id": "INV-0042", "title": "second"}`)
	if code != http.StatusConflict || errCode(out) != "id_taken" {
		t.Errorf("duplicate: %d %v", code, out)
	}
	if _, got := f.as(t, f.ada, "GET", "/v1/acme/app/invoices/INV-0042", ""); got["title"] != "first" {
		t.Errorf("overwritten: %v", got)
	}

	for name, tc := range map[string]struct {
		path, body, code string
		status           int
	}{
		"missing":          {"/v1/acme/app/invoices", `{"title": "x"}`, "id_required", 400},
		"null":             {"/v1/acme/app/invoices", `{"id": null, "title": "x"}`, "id_required", 400},
		"not a string":     {"/v1/acme/app/invoices", `{"id": 7, "title": "x"}`, "invalid_id", 400},
		"not the pattern":  {"/v1/acme/app/invoices", `{"id": "INV-12", "title": "x"}`, "invalid_id", 400},
		"partial match":    {"/v1/acme/app/invoices", `{"id": "xINV-0001", "title": "x"}`, "invalid_id", 400},
		"outside envelope": {"/v1/acme/app/cards", `{"id": "_files", "title": "x"}`, "invalid_id", 400},
		"not the user":     {"/v1/acme/app/cards", `{"id": "u_somebody", "title": "x"}`, "invalid_id", 400},
		"not allowed":      {"/v1/acme/app/posts", `{"id": "mine", "title": "x"}`, "id_not_allowed", 400},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := post(f.ada, tc.path, tc.body)
			if code != tc.status || errCode(out) != tc.code {
				t.Errorf("%d %v, want %d %s", code, out, tc.status, tc.code)
			}
			if strings.Contains(out["error"].(map[string]any)["message"].(string), "somebody") {
				t.Errorf("echoes the id: %v", out)
			}
		})
	}

	// The create rule runs before the existence check, and sees data.id: a caller who can't
	// create learns nothing about which ids exist.
	f.doH(t, "POST", "/v1/acme/app/invoices", `{"id": "INV-0001", "title": "x"}`, bearer(f.ada))
	if rec, out := f.doH(t, "POST", "/v1/acme/app/invoices", `{"id": "INV-0001", "title": "x"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous duplicate: %d %v", rec.Code, out)
	}
	if code, out := post(f.ada, "/v1/acme/app/invoices", `{"id": "INV-9999", "title": "x"}`); code != http.StatusForbidden {
		t.Errorf("a rule on data.id: %d %v", code, out)
	}

	// custom_id: user, the id is the user's own, once.
	code, out = post(f.ada, "/v1/acme/app/cards", `{"id": "`+f.adaID+`", "title": "ada"}`)
	if code != http.StatusCreated || out["id"] != f.adaID {
		t.Fatalf("card: %d %v", code, out)
	}
	if code, out = post(f.ada, "/v1/acme/app/cards", `{"id": "`+f.adaID+`", "title": "again"}`); code != http.StatusConflict || errCode(out) != "id_taken" {
		t.Errorf("second card: %d %v", code, out)
	}

	// PUT and PATCH never create.
	if code, _ := f.as(t, f.ada, "PUT", "/v1/acme/app/invoices/INV-5555", `{"title": "x"}`); code != http.StatusNotFound {
		t.Errorf("PUT of a missing id: %d", code)
	}
	if code, _ := f.as(t, f.ada, "PATCH", "/v1/acme/app/invoices/INV-5555", `{"title": "x"}`); code != http.StatusNotFound {
		t.Errorf("PATCH of a missing id: %d", code)
	}
}

func TestDuplicateAttemptsAreLimited(t *testing.T) {
	f := newRulesFixture(t)
	f.doH(t, "POST", "/v1/acme/app/invoices", `{"id": "INV-0001", "title": "x"}`, bearer(f.ada))
	// duplicate_limit is 3: three duplicates answer 409, then the caller is held.
	for i := 0; i < 3; i++ {
		if code, out := f.as(t, f.bob, "POST", "/v1/acme/app/invoices", `{"id": "INV-0001", "title": "x"}`); code != 409 {
			t.Fatalf("attempt %d: %d %v", i, code, out)
		}
	}
	rec, out := f.doH(t, "POST", "/v1/acme/app/invoices", `{"id": "INV-0002", "title": "x"}`, bearer(f.bob))
	if rec.Code != http.StatusTooManyRequests || errCode(out) != "too_many_requests" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("held: %d %v %v", rec.Code, out, rec.Header())
	}
	// Another caller is not held, and the minute passes.
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/app/invoices", `{"id": "INV-0003", "title": "x"}`); code != 201 {
		t.Errorf("another caller: %d", code)
	}
	*f.clock = f.clock.Add(61 * time.Second)
	if code, _ := f.as(t, f.bob, "POST", "/v1/acme/app/invoices", `{"id": "INV-0004", "title": "x"}`); code != 201 {
		t.Errorf("after a minute: %d", code)
	}
}

func TestBatchCreatesWithCustomIDs(t *testing.T) {
	f := newRulesFixture(t)
	batch := func(body string) (int, map[string]any) {
		return f.as(t, f.ada, "POST", "/v1/acme/app/_batch", body)
	}
	code, out := batch(`{"operations": [
		{"op": "create", "collection": "invoices", "id": "INV-0100", "document": {"title": "a"}},
		{"op": "create", "collection": "posts", "document": {"title": "b"}}]}`)
	if code != http.StatusOK {
		t.Fatalf("batch: %d %v", code, out)
	}
	if code, got := f.as(t, f.ada, "GET", "/v1/acme/app/invoices/INV-0100", ""); code != 200 || got["title"] != "a" {
		t.Errorf("created: %d %v", code, got)
	}
	for name, tc := range map[string]struct {
		body, code string
		status     int
	}{
		"missing":    {`{"operations": [{"op": "create", "collection": "invoices", "document": {"title": "a"}}]}`, "id_required", 400},
		"invalid":    {`{"operations": [{"op": "create", "collection": "invoices", "id": "nope", "document": {"title": "a"}}]}`, "invalid_id", 400},
		"not wanted": {`{"operations": [{"op": "create", "collection": "posts", "id": "mine", "document": {"title": "a"}}]}`, "id_not_allowed", 400},
		"taken":      {`{"operations": [{"op": "create", "collection": "invoices", "id": "INV-0100", "document": {"title": "a"}}]}`, "id_taken", 409},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := batch(tc.body)
			if code != tc.status || errCode(out) != tc.code {
				t.Errorf("%d %v, want %d %s", code, out, tc.status, tc.code)
			}
		})
	}
}
