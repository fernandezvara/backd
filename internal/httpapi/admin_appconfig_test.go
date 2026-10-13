package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The app configuration of the acme fixture realm (app-config/).
const appConfigSchema = `{
  "type": "object", "additionalProperties": false,
  "properties": {
    "maintenance": {"type": "object", "additionalProperties": false, "properties": {
      "enabled": {"type": "boolean", "default": false},
      "message": {"type": "string", "maxLength": 100, "default": ""}
    }},
    "features": {"type": "object", "additionalProperties": false, "properties": {
      "export": {"type": "object", "additionalProperties": false, "properties": {
        "enabled": {"type": "boolean", "default": true},
        "max_rows": {"type": "integer", "minimum": 1, "default": 1000}
      }}
    }},
    "internal": {"type": "object", "additionalProperties": false, "properties": {
      "ratio": {"type": "number", "default": 0.5}
    }}
  }
}`

const appConfigRules = `read:
  maintenance: "true"
  features: user != nil
`

func (f *rulesFixture) configDo(t *testing.T, method, path, body string, cred string, extra map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	h := jsonHdr(cred)
	for k, v := range extra {
		h[k] = v
	}
	rec, out := f.doH(t, method, admin+path, body, h)
	return rec.Code, out, rec.Header()
}

func TestAppConfigAdmin(t *testing.T) {
	f := newRulesFixture(t)
	ifm := func(v string) map[string]string { return map[string]string{"If-Match": v} }

	code, out, hdr := f.configDo(t, "GET", "/app-config", "", f.key, nil)
	if code != 200 || out["version"] != "c0" || hdr.Get("ETag") != `"c0"` {
		t.Fatalf("GET = %d %v %v", code, out, hdr)
	}
	values := out["values"].(map[string]any)
	if values["features"].(map[string]any)["export"].(map[string]any)["max_rows"] != float64(1000) {
		t.Errorf("values = %v", values)
	}
	keys := map[string]map[string]any{}
	for _, k := range out["keys"].([]any) {
		k := k.(map[string]any)
		keys[k["path"].(string)] = k
	}
	if keys["maintenance.enabled"]["public"] != true || keys["features.export.enabled"]["readable"] != true ||
		keys["features.export.enabled"]["public"] != false || keys["internal.ratio"]["readable"] != false {
		t.Errorf("keys = %v", keys)
	}

	// A change needs its base version.
	body := `{"set": {"maintenance.enabled": true, "maintenance.message": "Back at 10:00", "internal.ratio": 0.25}}`
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", body, f.key, nil); code != 428 || errCode(out) != "precondition_required" {
		t.Errorf("no If-Match = %d %v", code, out)
	}
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", body, f.key, ifm("nope")); code != 400 {
		t.Errorf("bad If-Match = %d", code)
	}
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", body, f.key, ifm(`"c7"`)); code != 412 {
		t.Errorf("stale If-Match = %d", code)
	}
	code, out, hdr = f.configDo(t, "PATCH", "/app-config", body, f.key, ifm(`"c0"`))
	if code != 200 || out["version"] != "c1" || out["changed"] != true || hdr.Get("ETag") != `"c1"` || len(out["changes"].([]any)) != 3 {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	// The same base is stale now; a refused change leaves the version alone.
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", body, f.key, ifm(`"c0"`)); code != 412 {
		t.Errorf("replayed PATCH = %d", code)
	}
	code, out, _ = f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.message": 5, "ghost": 1, "features": {}}}`, f.key, ifm(`"c1"`))
	if code != 400 || errCode(out) != "validation_error" {
		t.Fatalf("invalid PATCH = %d %v", code, out)
	}
	details := out["error"].(map[string]any)["details"].([]any)
	if len(details) < 2 {
		t.Errorf("details = %v", details)
	}
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", `{}`, f.key, ifm(`"c1"`)); code != 400 {
		t.Errorf("empty PATCH = %d %v", code, out)
	}
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", `{"nope": 1}`, f.key, ifm(`"c1"`)); code != 400 {
		t.Errorf("unknown field = %d", code)
	}

	// Setting what is there changes nothing; reset returns to the defaults.
	code, out, _ = f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.enabled": true}}`, f.key, ifm(`"c1"`))
	if code != 200 || out["version"] != "c1" || out["changed"] != false {
		t.Errorf("no-op = %d %v", code, out)
	}
	code, out, _ = f.configDo(t, "PATCH", "/app-config", `{"reset": ["maintenance"], "set": {"features.export.enabled": false}}`, f.key, ifm(`"c1"`))
	if code != 200 || out["version"] != "c2" {
		t.Fatalf("reset = %d %v", code, out)
	}

	// History, newest first, with who changed what; rollback is a new version.
	_, out, _ = f.configDo(t, "GET", "/app-config/history", "", f.key, nil)
	hist := out["history"].([]any)
	if len(hist) != 2 || hist[0].(map[string]any)["version"] != "c2" || !strings.HasPrefix(hist[0].(map[string]any)["actor"].(string), "key:") {
		t.Errorf("history = %v", hist)
	}
	if code, _, _ := f.configDo(t, "GET", "/app-config/history?limit=0", "", f.key, nil); code != 400 {
		t.Errorf("limit 0 = %d", code)
	}
	code, out, _ = f.configDo(t, "POST", "/app-config/rollback", `{"version": "c1"}`, f.key, ifm(`"c2"`))
	if code != 200 || out["version"] != "c3" {
		t.Fatalf("rollback = %d %v", code, out)
	}
	_, out, _ = f.configDo(t, "GET", "/app-config", "", f.key, nil)
	if out["values"].(map[string]any)["maintenance"].(map[string]any)["enabled"] != true || out["version"] != "c3" {
		t.Errorf("after rollback = %v", out)
	}
	if h := out["history"].([]any); h[0].(map[string]any)["rolled_back_to"] != "c1" {
		t.Errorf("rollback entry = %v", h[0])
	}
	if code, _, _ := f.configDo(t, "POST", "/app-config/rollback", `{"version": "c99"}`, f.key, ifm(`"c3"`)); code != 404 {
		t.Errorf("unknown version = %d", code)
	}
	if code, _, _ := f.configDo(t, "POST", "/app-config/rollback", `{"version": "x"}`, f.key, ifm(`"c3"`)); code != 400 {
		t.Errorf("bad version = %d", code)
	}

	// Each change is audited by its paths, never its values.
	changes := f.auditActions(t, "appconfig.")
	if len(changes) != 3 {
		t.Fatalf("audit = %v", changes)
	}
	if s := toJSON(t, changes); strings.Contains(s, "Back at") {
		t.Errorf("a value is in the audit trail: %s", s)
	}
}

func TestAppConfigAdminRights(t *testing.T) {
	f := newRulesFixture(t)
	ifm := map[string]string{"If-Match": `"c0"`}
	// An administrator without the config area is refused; one who only reads can read, not change.
	f.bobHolds(t, "support")
	if code, _, _ := f.configDo(t, "GET", "/app-config", "", f.bob, nil); code != 403 {
		t.Errorf("support GET = %d", code)
	}
	f.bobHolds(t, "viewer")
	if code, _, _ := f.configDo(t, "GET", "/app-config", "", f.bob, nil); code != 200 {
		t.Errorf("viewer GET = %d", code)
	}
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.enabled": true}}`, f.bob, ifm); code != 403 {
		t.Errorf("viewer PATCH = %d %v", code, out)
	}
	f.bobHolds(t, "staff")
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.enabled": true}}`, f.bob, ifm); code != 200 {
		t.Errorf("staff PATCH = %d", code)
	}
	// Not an administrator at all.
	if code, _, _ := f.configDo(t, "GET", "/app-config", "", f.ada, nil); code != 403 {
		t.Errorf("user GET = %d", code)
	}
}
