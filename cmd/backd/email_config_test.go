package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

// A realm that configures email needs backd's public address, for the links
// in its emails: BACKD_URL, or email.public_url in its realm.yaml.
func TestEmailNeedsAPublicURL(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	build := func(realmYAML string) *registry.Registry {
		fnDir := "acme/notify/" + registry.FunctionsDir
		files := map[string]string{
			"acme/realm.yaml":                realmYAML,
			fnDir + "/deliver/function.yaml": "internal: true\nmode: async\n",
			fnDir + "/deliver/index.js":      "",
		}
		for _, kind := range email.SystemKinds {
			defaults, _ := email.DefaultFiles(kind)
			for name, data := range defaults {
				files["acme/email/"+kind+"/"+name] = string(data)
			}
		}
		root := writeConfig(t, files)
		writeFuncManifest(t, filepath.Join(root, fnDir), "deliver")
		reg, err := registry.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	base := "email:\n  function: notify/deliver\n  from: no-reply@acme.example\n"

	if err := checkFunctions(build(base), settings.Settings{}, log); err == nil || !strings.Contains(err.Error(), "BACKD_URL") || !strings.Contains(err.Error(), "acme") {
		t.Errorf("without a public address: %v", err)
	}
	if err := checkFunctions(build(base), settings.Settings{BackdURL: "https://api.acme.example"}, log); err != nil {
		t.Errorf("with BACKD_URL: %v", err)
	}
	if err := checkFunctions(build(base+"  public_url: https://api.acme.example\n"), settings.Settings{}, log); err != nil {
		t.Errorf("with email.public_url: %v", err)
	}
}

func TestBackdURLSetting(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			return map[string]string{"CONFIG_DIR": "/c", "MONGO_URI": "mongodb://x", "BACKD_URL": v}[k]
		}
	}
	if s, err := settings.Load(env("https://api.acme.example/")); err != nil || s.BackdURL != "https://api.acme.example" {
		t.Errorf("BACKD_URL = %q, %v", s.BackdURL, err)
	}
	if _, err := settings.Load(env("api.acme.example")); err == nil || !strings.Contains(err.Error(), "BACKD_URL") {
		t.Errorf("a bad BACKD_URL: %v", err)
	}
}
