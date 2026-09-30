package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestRunConfig(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"CONFIG_DIR": dir}
	cli := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(args, func(k string) string { return env[k] }, nil, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	cli("template", "realm", "--realm", "shop", "--sample")

	// The sample database now includes a sample function (roadmap F16):
	// unbuilt bundles fail the check, like startup, exactly as adding a
	// fresh one with `template function` would.
	code, _, errOut := cli("config", "check")
	if code != 1 || !strings.Contains(errOut, "functions are not built: run `backd functions build` (for shop/main)") {
		t.Errorf("unbuilt functions: %d %q", code, errOut)
	}
	if code, _, _ := cli("config", "fingerprint"); code != 1 {
		t.Error("fingerprint of a config that wouldn't start")
	}

	// Built bundles pass the check, and the fingerprint matches what the
	// registry itself computes. Needs Deno.
	bin := os.Getenv("DENO")
	if bin == "" {
		bin = "deno"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("deno not found; skipping the built-bundle assertions (runs in the dockerized test image)")
	}
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := functions.Build(context.Background(), reg, functions.Options{Deno: bin}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := cli("config", "check")
	if code != 0 || !strings.Contains(out, "config OK: 1 realms, 1 databases; fingerprint ") {
		t.Errorf("check: %d %q", code, out)
	}
	code, fp, _ := cli("config", "fingerprint")
	reg, _ = registry.Load(dir)
	want, _ := reg.Fingerprint()
	if code != 0 || strings.TrimSpace(fp) != want || !strings.Contains(out, want) {
		t.Errorf("fingerprint: %d %q, want %s", code, fp, want)
	}

	// An invalid config fails with startup's message.
	os.WriteFile(filepath.Join(dir, "shop", "realm.yaml"), []byte("signup: public\n"), 0o644)
	if code, _, errOut := cli("config", "check"); code != 1 || !strings.Contains(errOut, `signup: must be open, invite or closed, got "public"`) {
		t.Errorf("invalid: %d %q", code, errOut)
	}

	for _, tt := range []struct {
		args []string
		env  map[string]string
		code int
		want string
	}{
		{[]string{"config"}, env, 0, "Usage: backd config"},
		{[]string{"config", "help"}, env, 0, "Usage: backd config"},
		{[]string{"config", "diff"}, env, 2, `unknown command "diff"`},
		{[]string{"config", "check", "x"}, env, 2, "unexpected arguments"},
		{[]string{"config", "check"}, nil, 1, "CONFIG_DIR is required"},
	} {
		var out bytes.Buffer
		code := run(tt.args, func(k string) string { return tt.env[k] }, nil, &out, &out)
		if code != tt.code || !strings.Contains(out.String(), tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out.String(), tt.code, tt.want)
		}
	}
}
