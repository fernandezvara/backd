package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppConfigCommands(t *testing.T) {
	type call struct {
		method, path, ifMatch string
		body                  map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path + "?" + r.URL.RawQuery, ifMatch: r.Header.Get("If-Match")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &c.body)
		}
		calls = append(calls, c)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/acme/_admin/app-config":
			w.Header().Set("ETag", `"c4"`)
			_, _ = w.Write([]byte(`{"version":"c4","values":{"maintenance":{"enabled":true,"message":"Back at 10:00"},"features":{"export":{"enabled":true}}},
				"overrides":{"maintenance":{"enabled":true}},"invalid":[{"path":"old.key","reason":"is not declared in the schema any more"}],"keys":[],"history":[]}`))
		case r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"version":"c5","changed":true,"changes":[{"path":"maintenance.enabled","old":true,"new":false}]}`))
		case r.URL.Path == "/v1/acme/_admin/app-config/rollback":
			_, _ = w.Write([]byte(`{"version":"c5","changed":false,"changes":[]}`))
		case r.URL.Path == "/v1/acme/_admin/app-config/history":
			_, _ = w.Write([]byte(`{"history":[{"version":"c4","at":"2026-10-01T10:00:00.000Z","actor":"user:u1","kind":"rollback","rolled_back_to":"c2","changes":[{"path":"a.b","old":1,"new":2}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	base := []string{"--realm", "acme", "--url", srv.URL}
	with := func(args ...string) []string { return append(args, base...) }

	out := c.expect(0, "version c4", "", with("app-config", "get")...)
	for _, want := range []string{"* maintenance.enabled", "  features.export.enabled", "old.key is not declared"} {
		if !strings.Contains(out, want) {
			t.Errorf("get lacks %q:\n%s", want, out)
		}
	}
	c.expect(0, `"maintenance.message":"Back at 10:00"`, "", with("app-config", "get", "--key", "maintenance", "--json")...)
	c.expect(1, "not a configuration key", "", with("app-config", "get", "--key", "nope")...)

	calls = nil
	c.expect(0, "maintenance.enabled: true -> false", "", with("app-config", "set", "--key", "maintenance.message", "--value", "Back at 11:00")...)
	if len(calls) != 2 || calls[1].method != "PATCH" || calls[1].ifMatch != `"c4"` {
		t.Fatalf("calls = %+v", calls)
	}
	if set := calls[1].body["set"].(map[string]any); set["maintenance.message"] != "Back at 11:00" {
		t.Errorf("body = %v", calls[1].body)
	}
	c.expect(0, "version c5", "", with("app-config", "set", "--key", "maintenance.enabled", "--value", "false", "--if-match", "c9")...)
	if last := calls[len(calls)-1]; last.ifMatch != `"c9"` || last.body["set"].(map[string]any)["maintenance.enabled"] != false {
		t.Errorf("typed value = %+v", last)
	}
	c.expect(0, "version c5", "", with("app-config", "set", "--values", `{"a.b": 1, "c.d": "x"}`)...)
	c.expect(1, "--key and --value", "", with("app-config", "set")...)
	c.expect(0, "version c5", "", with("app-config", "reset", "--keys", "maintenance, features.export")...)
	if reset := calls[len(calls)-1].body["reset"].([]any); len(reset) != 2 || reset[1] != "features.export" {
		t.Errorf("reset = %v", reset)
	}
	c.expect(0, "nothing changed", "", with("app-config", "rollback", "--version", "c2")...)
	if last := calls[len(calls)-1]; last.body["version"] != "c2" || last.ifMatch != `"c4"` {
		t.Errorf("rollback = %+v", last)
	}
	c.expect(0, "(rollback to c2)", "", with("app-config", "history")...)
}
