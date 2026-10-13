package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestComplianceScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/acme/_admin/compliance/scope" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"realm":"acme","auth":true,"collections":[
			{"database":"app","collection":"orders","export":"include","owned":true,"pull":["members"],"files":["receipt"],"declared":false},
			{"database":"app","collection":"scores","export":"exclude","owned":false,"pull":[],"files":[],"reason":"fraud scores","declared":true},
			{"database":"app","collection":"notes","export":"include","owned":true,"fields":["title"],"reason":"the rest is internal","pull":[],"files":[],"declared":true}],
			"system":{"included":["account: id, email"],"excluded":["password hashes"]}}`))
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := c.expect(0, "app/scores", "", "compliance", "scope", "--realm", "acme", "--url", srv.URL)
	for _, want := range []string{
		"app/orders", "documents naming the user in members", "files of receipt",
		"excluded: fraud scores", "fields title (the rest is internal)", "account: id, email", "password hashes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scope lacks %q:\n%s", want, out)
		}
	}
	c.expect(0, `"export":"exclude"`, "", "compliance", "scope", "--realm", "acme", "--url", srv.URL, "--json")
}

func TestConsentsNotify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/acme/_admin/consents/notify" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"consent":"terms","version":"2026-10","matched":5,"queued":3,"limited":2}`))
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := c.expect(0, "terms 2026-10: 5 users hold an older version; 3 emails queued, 2 skipped", "", "consents", "notify", "--realm", "acme", "--consent", "terms", "--url", srv.URL)
	_ = out
	if got["consent"] != "terms" {
		t.Errorf("body = %v", got)
	}
	c.expect(0, `"queued":3`, "", "consents", "notify", "--realm", "acme", "--consent", "terms", "--url", srv.URL, "--json")
	c.expect(2, "", "consent", "consents", "notify", "--realm", "acme", "--url", srv.URL)
}
