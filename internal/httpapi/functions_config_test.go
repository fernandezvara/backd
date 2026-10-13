package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// configCall sends a request to the internal listener as a function would.
func (f *rulesFixture) configCall(t *testing.T, method, path, body string, claims auth.CallbackClaims) (int, map[string]any) {
	t.Helper()
	claims.Realm, claims.Expires = "acme", f.clock.Add(time.Minute)
	if claims.Function == "" {
		claims.Function = "app/promo"
	}
	req := httptest.NewRequest(method, "/v1/acme/_fn/config"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+auth.SignCallback(f.callbackKey, claims))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestFunctionsReadAndChangeConfig(t *testing.T) {
	f := newRulesFixture(t)

	// Any function reads every value, whatever the clients' rules allow.
	code, out := f.configCall(t, "GET", "", "", auth.CallbackClaims{})
	internal := out["values"].(map[string]any)["internal"].(map[string]any)
	if code != 200 || internal["ratio"] != 0.5 || out["version"] != "c0" {
		t.Fatalf("read = %d %v", code, out)
	}

	// Changing needs the grant in the token.
	set := `{"set": {"maintenance.enabled": true, "features.export.max_rows": 50}}`
	if code, out := f.configCall(t, "PATCH", "", set, auth.CallbackClaims{}); code != 403 {
		t.Errorf("without the grant = %d %v", code, out)
	}
	code, out = f.configCall(t, "PATCH", "", set, auth.CallbackClaims{Config: true})
	if code != 200 || out["version"] != "c1" || out["changed"] != true {
		t.Fatalf("set = %d %v", code, out)
	}
	if code, out := f.configCall(t, "PATCH", "", `{"set": {"features.export.max_rows": "many"}}`, auth.CallbackClaims{Config: true}); code != 400 || errCode(out) != "validation_error" {
		t.Errorf("invalid set = %d %v", code, out)
	}
	code, out = f.configCall(t, "PATCH", "", `{"reset": ["features"]}`, auth.CallbackClaims{Config: true})
	if code != 200 || out["version"] != "c2" {
		t.Fatalf("reset = %d %v", code, out)
	}

	// The function is the actor, in the history and in the audit trail.
	code, out = f.configCall(t, "GET", "/history", "", auth.CallbackClaims{Config: true})
	hist := out["history"].([]any)
	if code != 200 || len(hist) != 2 || hist[0].(map[string]any)["actor"] != "function:app/promo" {
		t.Errorf("history = %d %v", code, out)
	}
	if code, _ := f.configCall(t, "GET", "/history", "", auth.CallbackClaims{}); code != 403 {
		t.Errorf("history without the grant = %d", code)
	}
	trail := f.auditActions(t, "appconfig.")
	if len(trail) != 2 || trail[0]["actor"] != "function:app/promo" {
		t.Errorf("audit = %v", trail)
	}

	// Not a function: a user's session token isn't accepted on the internal listener.
	rec, _ := f.doH(t, "GET", "/v1/acme/_fn/config", "", bearer(f.ada))
	if rec.Code == 200 {
		t.Error("the public listener serves the function route")
	}
}

// A kill switch enforced in a collection's rules: the value is read for the request.
func TestAppConfigInCollectionRules(t *testing.T) {
	f := newRulesFixture(t)
	exports := "/v1/acme/app/exports"
	doc := `{"title": "q3", "published": true}`

	rec, out := f.doH(t, "POST", exports, doc, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("with the flag at its default (true) ada creates: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	if rec, _ := f.doH(t, "GET", exports+"/"+id, "", bearer(f.ada)); rec.Code != 200 {
		t.Errorf("read with the flag on = %d", rec.Code)
	}

	// An administrator turns the switch off: the server refuses at once, whatever the app shows.
	patch := func(v string, base string) {
		t.Helper()
		code, out, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"features.export.enabled": `+v+`}}`, f.key, map[string]string{"If-Match": base})
		if code != 200 {
			t.Fatalf("PATCH = %d %v", code, out)
		}
	}
	patch("false", `"c0"`)
	if rec, _ := f.doH(t, "POST", exports, doc, jsonHdr(f.ada)); rec.Code != 403 {
		t.Errorf("create with the flag off = %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", exports+"?limit=10", "", bearer(f.ada)); rec.Code != 403 {
		t.Errorf("list with the flag off = %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", exports+"/"+id, "", bearer(f.ada)); rec.Code != 403 {
		t.Errorf("get with the flag off = %d", rec.Code)
	}
	patch("true", `"c1"`)
	if rec, _ := f.doH(t, "GET", exports+"/"+id, "", bearer(f.ada)); rec.Code != 200 {
		t.Errorf("get with the flag back on = %d", rec.Code)
	}
}
