package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

func TestRunTemplateFunction(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"CONFIG_DIR": dir}
	cli := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(args, func(k string) string { return env[k] }, nil, &out, &out)
		return code, out.String()
	}
	if code, out := cli("template", "function", "--realm", "shop", "--database", "app", "--name", "hello"); code != 1 || !strings.Contains(out, "database shop/app doesn't exist") {
		t.Errorf("without the database: %d %q", code, out)
	}
	cli("template", "realm", "--realm", "shop")
	cli("template", "database", "--realm", "shop", "--database", "app")
	code, out := cli("template", "function", "--realm", "shop", "--database", "app", "--name", "hello")
	for _, want := range []string{"created   shop/app/_functions/deno.json", "created   shop/app/_functions/hello/function.yaml", "created   shop/app/_functions/hello/index.ts"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("template function: %d, want %q in\n%s", code, want, out)
		}
	}
	// A second function shares the project's deno.json.
	if code, out := cli("template", "function", "--realm", "shop", "--database", "app", "--name", "other"); code != 0 || !strings.Contains(out, "exists    shop/app/_functions/deno.json") {
		t.Errorf("second function: %d\n%s", code, out)
	}
	// The generated function is valid config (only the bundles are missing).
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatalf("template produced invalid config: %v", err)
	}
	if fn := reg.Realms["shop"].Databases["app"].Functions.Functions["hello"]; fn == nil || fn.Invoke != nil || fn.Timeout != registry.DefaultSyncTimeout {
		t.Errorf("template function = %+v", fn)
	}
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"template", "function", "--realm", "shop", "--database", "app", "--name", "lib"}, 1, "not lib, which is reserved"},
		{[]string{"template", "function", "--realm", "shop", "--database", "app", "--name", "Bad"}, 1, `invalid function name "Bad"`},
		{[]string{"template", "function", "--realm", "shop", "--database", "app"}, 2, "--name"},
		{[]string{"template", "function", "--realm", "shop", "--database", "app", "--name", "x", "--sample"}, 2, "sample"},
	} {
		if code, out := cli(tt.args...); code != tt.code || !strings.Contains(out, tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out, tt.code, tt.want)
		}
	}
}

func TestRunFunctionsArguments(t *testing.T) {
	empty := t.TempDir()
	for _, tt := range []struct {
		args []string
		env  map[string]string
		code int
		want string
	}{
		{[]string{"functions"}, nil, 0, "Usage: backd functions"},
		{[]string{"functions", "help"}, nil, 0, "Usage: backd functions"},
		{[]string{"functions", "deploy"}, nil, 2, `unknown command "deploy"`},
		{[]string{"functions", "build", "extra"}, nil, 2, "unexpected arguments"},
		{[]string{"functions", "build", "--force"}, nil, 2, "flag provided but not defined: -force"},
		{[]string{"functions", "build"}, nil, 1, "CONFIG_DIR is required"},
		{[]string{"functions", "build"}, map[string]string{"CONFIG_DIR": empty}, 0, "no functions in CONFIG_DIR"},
		{[]string{"functions", "invoke"}, nil, 2, "--function string (required) -> Not provided"},
		{[]string{"functions", "invoke", "--function", "acme"}, nil, 2, `want <realm>/<database>/<name>, got "acme"`},
		{[]string{"functions", "invoke", "--function", "acme/app/hi", "--input", "/nonexistent"}, nil, 1, "no such file or directory"},
		{[]string{"functions", "types", "extra"}, nil, 2, "unexpected arguments"},
		{[]string{"functions", "types"}, nil, 1, "CONFIG_DIR is required"},
		{[]string{"functions", "types"}, map[string]string{"CONFIG_DIR": empty}, 0, "no function declares"},
	} {
		var out bytes.Buffer
		code := run(tt.args, func(k string) string { return tt.env[k] }, nil, &out, &out)
		if code != tt.code || !strings.Contains(out.String(), tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out.String(), tt.code, tt.want)
		}
	}
}

func TestRunTemplateProject(t *testing.T) {
	root := t.TempDir()
	cli := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(args, func(string) string { return "" }, nil, &out, &out)
		return code, out.String()
	}

	dir := filepath.Join(root, "my-app")
	code, out := cli("template", "project", "--dir", dir, "--realm", "shop")
	for _, want := range []string{
		"created   config/shop/realm.yaml",
		"created   config/shop/main/posts/schema.json",
		"created   config/shop/main/_functions/stats/index.ts",
		"created   Dockerfile",
		"created   compose.yaml",
		"created   .github/workflows/ci.yml",
		"created   README.md",
		"next: cd " + dir,
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("template project: %d, want %q in\n%s", code, want, out)
		}
	}
	if _, err := registry.Load(filepath.Join(dir, "config")); err != nil {
		t.Errorf("generated config doesn't load: %v", err)
	}

	// A second run over the same (now non-empty) directory is refused.
	if code, out := cli("template", "project", "--dir", dir, "--realm", "shop"); code != 1 || !strings.Contains(out, "already exists and isn't an empty directory") {
		t.Errorf("existing directory: %d %q", code, out)
	}

	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"template", "project", "--dir", filepath.Join(root, "other"), "--realm", "Shop"}, 1, `invalid realm name "Shop"`},
		{[]string{"template", "project", "--dir", filepath.Join(root, "other"), "--realm", "shop", "--sample"}, 2, "sample"},
		{[]string{"template", "project", "--dir", dir}, 2, "--realm"},
	} {
		if code, out := cli(tt.args...); code != tt.code || !strings.Contains(out, tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out, tt.code, tt.want)
		}
	}
}

// checkFunctions: bundles must be built and fresh.
func TestCheckFunctions(t *testing.T) {
	dir := t.TempDir()
	fns := filepath.Join(dir, "shop", "app", registry.FunctionsDir)
	for p, c := range map[string]string{
		"shop/realm.yaml": "",
		"open/realm.yaml": "auth: disabled\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	os.MkdirAll(filepath.Join(fns, "hello"), 0o755)
	os.WriteFile(filepath.Join(fns, "hello", "function.yaml"), nil, 0o644)
	os.WriteFile(filepath.Join(fns, "hello", "index.js"), []byte("export default () => 1;\n"), 0o644)

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	load := func() *registry.Registry {
		reg, err := registry.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	if err := checkFunctions(load(), settings.Settings{}, log); err == nil || !strings.Contains(err.Error(), "functions are not built") {
		t.Errorf("unbuilt: %v", err)
	}
	// A build as `backd functions build` writes it.
	src, _ := registry.SourceHash(fns)
	bundle := []byte("export default () => 1;\n")
	sum := sha256.Sum256(bundle)
	os.MkdirAll(filepath.Join(fns, registry.BuildDir), 0o755)
	os.WriteFile(filepath.Join(fns, registry.BuildDir, "hello-x.js"), bundle, 0o644)
	m, _ := json.Marshal(registry.Manifest{Deno: registry.DenoVersion, Source: src, Functions: map[string]registry.ManifestBundle{"hello": {Bundle: "hello-x.js", SHA256: hex.EncodeToString(sum[:])}}})
	os.WriteFile(filepath.Join(fns, registry.BuildDir, registry.ManifestFile), m, 0o644)

	// Functions in realms with auth need no flag; the security review is done.
	if err := checkFunctions(load(), settings.Settings{}, log); err != nil {
		t.Errorf("built: %v", err)
	}
	// Realms without auth build the same way.
	os.Rename(filepath.Join(dir, "shop", "app"), filepath.Join(dir, "open", "app"))
	if err := checkFunctions(load(), settings.Settings{}, slog.New(slog.NewJSONHandler(io.Discard, nil))); err != nil {
		t.Errorf("auth disabled: %v", err)
	}
}

func TestWarnAnonymousFunctionsWithoutRateLimit(t *testing.T) {
	dir := t.TempDir()
	fns := filepath.Join(dir, "shop", "app", registry.FunctionsDir)
	os.MkdirAll(filepath.Join(dir, "shop"), 0o755)
	os.WriteFile(filepath.Join(dir, "shop", "realm.yaml"), nil, 0o644)
	for name, fnYAML := range map[string]string{
		"open":      "invoke: \"true\"\n", // anonymous allowed, no rate_limit: warn
		"limited":   "invoke: \"true\"\nrate_limit:\n  per: ip\n  limit: 10\n  window: 1m\n",
		"needsuser": "invoke: \"user != nil\"\n", // anonymous never allowed: no warning
		"keyonly":   "",                          // no invoke rule: only API keys, never anonymous
	} {
		d := filepath.Join(fns, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "function.yaml"), []byte(fnYAML), 0o644)
		os.WriteFile(filepath.Join(d, "index.js"), nil, 0o644)
	}
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	warnAnonymousFunctionsWithoutRateLimit(reg, slog.New(slog.NewJSONHandler(&logs, nil)))
	out := logs.String()
	if !strings.Contains(out, `"function":"shop/app/open"`) {
		t.Errorf("no warning for shop/app/open:\n%s", out)
	}
	for _, fn := range []string{"limited", "needsuser", "keyonly"} {
		if strings.Contains(out, `"function":"shop/app/`+fn+`"`) {
			t.Errorf("unexpected warning for shop/app/%s:\n%s", fn, out)
		}
	}
}

func TestRunExecutorArguments(t *testing.T) {
	for _, tt := range []struct {
		args []string
		env  map[string]string
		code int
		want string
	}{
		{[]string{"executor", "help"}, nil, 0, "Usage: backd executor"},
		{[]string{"executor", "extra"}, nil, 2, "unexpected arguments"},
		{[]string{"executor"}, map[string]string{"DENO": "/nonexistent/deno"}, 1, "can't run /nonexistent/deno"},
		{[]string{"executor"}, map[string]string{"EXECUTOR_MAX_PROCESSES": "0"}, 1, "EXECUTOR_MAX_PROCESSES must be a positive integer"},
		{[]string{"executor"}, map[string]string{"LOG_LEVEL": "loud"}, 1, "LOG_LEVEL must be"},
	} {
		var out bytes.Buffer
		code := run(tt.args, func(k string) string { return tt.env[k] }, nil, &out, &out)
		if code != tt.code || !strings.Contains(out.String(), tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out.String(), tt.code, tt.want)
		}
	}
}
