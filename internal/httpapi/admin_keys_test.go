package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAdminKeysAreShownOnceAndNeverAgain(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	_, adminKey, err := f.svc.CreateAPIKey(context.Background(), "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := jsonHdr(adminKey)
	url := admin + "/secrets"

	// Rights: a session needs a fresh proof, and a partial administrator is refused.
	f.bobHolds(t, "keeper")
	if rec, _ := f.doH(t, "POST", url, `{"name":"HEALTH","type":"key","generate":true}`, f.withProof(t, f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a partial administrator: %d", rec.Code)
	}
	f.bobHolds(t, "staff")
	rec, out := f.doH(t, "POST", url, `{"name":"HEALTH","type":"key","generate":true}`, jsonHdr(f.bob))
	if rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("a session without proof: %d %v", rec.Code, out)
	}

	for body, want := range map[string]string{
		`{"name":"HEALTH","type":"secret","generate":true}`:          "type",
		`{"name":"health","type":"key","generate":true}`:             "name",
		`{"name":"HEALTH","type":"key"}`:                             "generate",
		`{"name":"HEALTH","type":"key","generate":true,"value":"x"}`: "generate",
		`{"name":"HEALTH","type":"key","value":"too-short"}`:         "value",
	} {
		rec, out := f.doH(t, "POST", url, body, key)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %v", body, rec.Code, out)
		}
	}

	// A generated key comes back once, with its fingerprint; a session with proof does it too.
	rec, out = f.doH(t, "POST", url, `{"name":"HEALTH","type":"key","generate":true}`, f.withProof(t, f.bob))
	if rec.Code != http.StatusCreated || out["key"] == nil || out["fingerprint"] == "" || out["type"] != "key" {
		t.Fatalf("generate: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	value, fp := out["key"].(string), out["fingerprint"].(string)
	if rec, _ := f.doH(t, "POST", url, `{"name":"HEALTH","type":"key","generate":true}`, key); rec.Code != http.StatusConflict {
		t.Errorf("a second HEALTH: %d", rec.Code)
	}
	// An imported key answers with the fingerprint alone.
	rec, out = f.doH(t, "POST", url, `{"name":"TAX","type":"key","value":"`+value+`"}`, key)
	if rec.Code != http.StatusCreated || out["key"] != nil || out["fingerprint"] != fp {
		t.Errorf("import: %d %v", rec.Code, out)
	}

	// The listing has the fingerprint and where it is used, never the value; plain writes can't touch a key.
	rec, out = f.doH(t, "GET", url, "", key)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), value) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	items := out["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["fingerprint"] != fp || items[0].(map[string]any)["type"] != "key" || items[0].(map[string]any)["used_by"] == nil {
		t.Errorf("items = %v", items)
	}
	if rec, _ := f.doH(t, "PUT", url+"/HEALTH", `{"value":"x"}`, key); rec.Code != http.StatusConflict {
		t.Errorf("PUT over a key: %d", rec.Code)
	}

	// Deleting needs proof from a session; a key in use is refused.
	if rec, out := f.doH(t, "DELETE", url+"/TAX", "", jsonHdr(f.bob)); rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("delete without proof: %d %v", rec.Code, out)
	}
	f.svc.KeyUses = func(name string) []string {
		if name == "HEALTH" {
			return []string{"main/patients.notes"}
		}
		return nil
	}
	if rec, out := f.doH(t, "DELETE", url+"/HEALTH", "", key); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "main/patients.notes") {
		t.Errorf("delete in use: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", url+"/TAX", "", f.withProof(t, f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	// The trail names the key and its fingerprint, never the value.
	_, trail := f.doH(t, "GET", admin+"/audit?limit=100", "", key)
	seen := map[string]bool{}
	for _, it := range trail["items"].([]any) {
		m := it.(map[string]any)
		a, _ := m["action"].(string)
		seen[a] = true
	}
	if !seen[auth.AuditKeyCreate] || !seen[auth.AuditKeyDelete] {
		t.Errorf("audit actions = %v", seen)
	}
	raw, _ := f.doH(t, "GET", admin+"/audit?limit=100", "", key)
	if strings.Contains(raw.Body.String(), value) {
		t.Error("the audit trail holds the key")
	}
	if strings.Contains(f.log.String(), value) {
		t.Error("the log holds the key")
	}
}
