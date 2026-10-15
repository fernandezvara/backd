package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// A function's ctx.db reads as the caller and ctx.admin.db with full access: both go through
// the internal listener, where expand follows the same rules as for any caller.
func TestFunctionsExpandAsCallerAndAsAdmin(t *testing.T) {
	f := newRulesFixture(t)
	do := func(method, path, body, token string) (int, map[string]any, http.Header) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Header()
	}
	token := func(c auth.CallbackClaims) string {
		c.Realm, c.Function = "acme", "app/echo"
		c.Expires = f.clock.Add(time.Minute)
		return auth.SignCallback(f.callbackKey, c)
	}
	asAda, admin := token(auth.CallbackClaims{UserID: f.adaID}), token(auth.CallbackClaims{Admin: true})

	// A document of a collection nobody can read through rules, and a note that points at it
	// and at an archive document.
	_, private, _ := do("POST", "/v1/acme/app/private", `{"title": "classified"}`, admin)
	archived := mustCreate(t, do, "/v1/acme/app/archive", `{"title": "old"}`, asAda)
	note := mustCreate(t, do, "/v1/acme/app/notes", `{"title": "`+private["id"].(string)+`", "status": "`+archived+`"}`, asAda)

	code, got, h := do("GET", "/v1/acme/app/notes/"+note+"?expand=status(title),secret", "", asAda)
	exp, _ := got["_expanded"].(map[string]any)
	if code != 200 || exp["status"].(map[string]any)["title"] != "old" || exp["secret"] != nil {
		t.Fatalf("as the caller: %d %v", code, got)
	}
	if h.Get("X-Backd-Expand-Queries") != "2" || h.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("headers = %v", h)
	}

	// With admin access the same request reaches what rules would hide, on get and on list.
	code, got, _ = do("GET", "/v1/acme/app/notes/"+note+"?expand=secret(title)", "", admin)
	exp, _ = got["_expanded"].(map[string]any)
	if code != 200 || exp["secret"].(map[string]any)["title"] != "classified" {
		t.Fatalf("as admin: %d %v", code, got)
	}
	code, got, _ = do("GET", "/v1/acme/app/notes?expand=secret(title)&limit=100", "", admin)
	items, _ := got["items"].([]any)
	if code != 200 || len(items) == 0 || items[0].(map[string]any)["_expanded"] == nil {
		t.Fatalf("admin list: %d %v", code, got)
	}

	// An anonymous callback has no identity: the rules refuse before anything is expanded.
	if code, _, _ := do("GET", "/v1/acme/app/notes/"+note+"?expand=status", "", token(auth.CallbackClaims{})); code != http.StatusUnauthorized {
		t.Errorf("anonymous callback: %d", code)
	}
	// A wrong expand is the caller's error, not a server one.
	if code, out, _ := do("GET", "/v1/acme/app/notes/"+note+"?expand=nope", "", asAda); code != 400 || errCode(out) != "invalid_expand" {
		t.Errorf("invalid: %d %v", code, out)
	}
}

func mustCreate(t *testing.T, do func(method, path, body, token string) (int, map[string]any, http.Header), path, body, token string) string {
	t.Helper()
	code, out, _ := do("POST", path, body, token)
	if code != 201 {
		t.Fatalf("POST %s: %d %v", path, code, out)
	}
	return out["id"].(string)
}
