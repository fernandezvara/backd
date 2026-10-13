package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func (f *rulesFixture) readConfig(t *testing.T, cred, query string, hdr map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	h := map[string]string{}
	if cred != "" {
		h["Authorization"] = "Bearer " + cred
	}
	for k, v := range hdr {
		h[k] = v
	}
	rec, out := f.doH(t, "GET", "/v1/acme/_config"+query, "", h)
	return rec.Code, out, rec.Header()
}

func TestReadAppConfig(t *testing.T) {
	f := newRulesFixture(t)
	group := func(out map[string]any, path ...string) map[string]any {
		cur := out["values"].(map[string]any)
		for _, p := range path {
			next, ok := cur[p].(map[string]any)
			if !ok {
				return nil
			}
			cur = next
		}
		return cur
	}

	// Anonymous callers see the public branch, and a shared cache may keep it.
	code, out, hdr := f.readConfig(t, "", "", nil)
	if code != 200 {
		t.Fatalf("anonymous = %d %v", code, out)
	}
	if group(out, "maintenance")["enabled"] != false || group(out, "features") != nil || group(out, "internal") != nil {
		t.Errorf("anonymous values = %v", out["values"])
	}
	if out["version"] != "c0" || out["cache_ttl"] != float64(300) {
		t.Errorf("envelope = %v", out)
	}
	if hdr.Get("Cache-Control") != "public, max-age=300" || !strings.Contains(hdr.Get("Vary"), "Authorization") {
		t.Errorf("anonymous headers = %v", hdr)
	}
	tag := hdr.Get("ETag")
	if !strings.HasPrefix(tag, `W/"`) {
		t.Fatalf("ETag = %q", tag)
	}

	// Revalidation answers 304; a change of the values changes the tag.
	if code, _, _ := f.readConfig(t, "", "", map[string]string{"If-None-Match": tag}); code != 304 {
		t.Errorf("If-None-Match = %d", code)
	}

	// A signed-in user sees what their rules allow, privately.
	code, out, hdr = f.readConfig(t, f.ada, "", nil)
	if code != 200 {
		t.Fatalf("ada = %d %v", code, out)
	}
	// features.export needs staff, and every key of features lives under it: deeper rules only narrow.
	if group(out, "features") != nil || group(out, "maintenance") == nil {
		t.Errorf("ada's values = %v", out["values"])
	}
	if hdr.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("ada's Cache-Control = %q", hdr.Get("Cache-Control"))
	}
	if hdr.Get("ETag") == tag {
		t.Error("different values share a tag")
	}
	// Verified users see `internal` by the user.email_verified branch of its rule.
	if group(out, "internal")["ratio"] != 0.5 {
		t.Errorf("internal = %v", out["values"])
	}

	// A member of staff also sees the narrowed key.
	f.bobHolds(t, "staff")
	_, out, _ = f.readConfig(t, f.bob, "", nil)
	if group(out, "features", "export")["enabled"] != true {
		t.Errorf("staff values = %v", out["values"])
	}

	// paths= selects branches; unknown ones are refused; hidden ones are absent.
	_, out, _ = f.readConfig(t, "", "?paths=maintenance,features", nil)
	if group(out, "maintenance") == nil || group(out, "features") != nil || group(out, "internal") != nil {
		t.Errorf("paths = %v", out["values"])
	}
	code, out, _ = f.readConfig(t, "", "?paths=nope", nil)
	if code != 400 || errCode(out) != "invalid_path" {
		t.Errorf("unknown path = %d %v", code, out)
	}
	if _, out, _ = f.readConfig(t, "", "?paths=maintenance.message", nil); group(out, "maintenance")["message"] != "" || len(group(out, "maintenance")) != 1 {
		t.Errorf("leaf path = %v", out["values"])
	}

	// A change shows up with the new version and a new tag; an API key is not a user.
	body := `{"set": {"maintenance.enabled": true}}`
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", body, f.key, map[string]string{"If-Match": `"c0"`}); code != 200 {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	code, out, hdr = f.readConfig(t, "", "", map[string]string{"If-None-Match": tag})
	if code != 200 || group(out, "maintenance")["enabled"] != true || out["version"] != "c1" || hdr.Get("ETag") == tag {
		t.Errorf("after a change = %d %v %v", code, out, hdr)
	}
	_, out, _ = f.readConfig(t, f.key, "", nil)
	if group(out, "features") != nil || group(out, "maintenance") == nil {
		t.Errorf("a key reads as anonymous: %v", out["values"])
	}

	// Bad credentials are refused, as everywhere.
	if code, _, _ := f.readConfig(t, "bdk_nope", "", nil); code != 401 {
		t.Errorf("bad credential = %d", code)
	}
}
