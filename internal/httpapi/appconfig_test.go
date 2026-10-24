package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/realtime"
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

// A configuration subscription reports, per caller, only the keys they read that changed.
func TestConfigSubscriptionReportsWhatTheCallerReads(t *testing.T) {
	f := newRulesFixture(t)
	d := &documents{reg: f.reg, now: time.Now, users: func(string) *auth.Users { return f.svc }}
	mk := func(token string, paths ...string) *realtime.ConfigSub {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/acme/_realtime", nil)
		var caller auth.Caller
		hasAuth := token != ""
		if hasAuth {
			r.Header.Set("Authorization", "Bearer "+token)
			var err error
			if caller, err = f.svc.Identify(r.Context(), token); err != nil {
				t.Fatal(err)
			}
		}
		sub, problem := d.configSub(r, "acme", caller, hasAuth, "cfg", paths)
		if sub == nil {
			t.Fatalf("configSub: %s", problem)
		}
		return sub
	}
	anon, ada := mk(""), mk(f.ada)
	check := func(sub *realtime.ConfigSub) *realtime.ConfigNotice {
		t.Helper()
		n, err := sub.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if check(anon) != nil || check(ada) != nil {
		t.Fatal("nothing changed yet")
	}

	// A change of a public key reaches everyone, with the version it made.
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.enabled": true}}`, f.key, map[string]string{"If-Match": `"c0"`}); code != 200 {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	for who, sub := range map[string]*realtime.ConfigSub{"anonymous": anon, "ada": ada} {
		n := check(sub)
		if n == nil || n.Version != "c1" || !slices.Equal(n.Paths, []string{"maintenance.enabled"}) {
			t.Errorf("%s: %+v", who, n)
		}
	}

	// A key only staff read reaches nobody else, and the same change twice says nothing new.
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"features.export.enabled": false}}`, f.key, map[string]string{"If-Match": `"c1"`}); code != 200 {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	if check(anon) != nil || check(ada) != nil {
		t.Error("a hidden key was reported")
	}
	f.bobHolds(t, "staff")
	staff := mk(f.bob)
	if code, out, _ := f.configDo(t, "PATCH", "/app-config", `{"reset": ["features.export"]}`, f.key, map[string]string{"If-Match": `"c2"`}); code != 200 {
		t.Fatalf("PATCH = %d %v", code, out)
	}
	if n := check(staff); n == nil || n.Version != "c3" || !slices.Equal(n.Paths, []string{"features.export.enabled"}) {
		t.Errorf("staff: %+v", n)
	}

	// Following one branch ignores the others.
	branch := mk("", "maintenance")
	if check(branch) != nil {
		t.Error("the first look is the baseline")
	}
	if code, _, _ := f.configDo(t, "PATCH", "/app-config", `{"set": {"maintenance.message": "back soon"}}`, f.key, map[string]string{"If-Match": `"c3"`}); code != 200 {
		t.Fatal("PATCH")
	}
	if n := check(branch); n == nil || !slices.Equal(n.Paths, []string{"maintenance.message"}) {
		t.Errorf("branch: %+v", n)
	}

	// Unknown keys are refused when subscribing.
	if sub, problem := d.configSub(httptest.NewRequest("POST", "/", nil), "acme", auth.Caller{}, false, "c", []string{"nope"}); sub != nil || problem == "" {
		t.Errorf("unknown key: %v %q", sub, problem)
	}
}
