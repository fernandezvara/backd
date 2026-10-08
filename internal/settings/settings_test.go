package settings

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	s, err := Load(env(map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Settings{ConfigDir: "/cfg", MongoURI: "mongodb://m", HTTPAddr: ":8080", ProvisionMode: ProvisionApply, LogLevel: slog.LevelInfo, MaxBodyBytes: 1 << 20, MaxUploadBytes: 100 << 20, ImageMaxPixels: 40_000_000,
		MongoOpTimeout: 10 * time.Second, ShutdownTimeout: 15 * time.Second, AdminUIIdle: 30 * time.Minute, InternalAddr: ":8081", WorkerConcurrency: 10}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got %+v, want %+v", s, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	s, err := Load(env(map[string]string{
		"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m",
		"HTTP_ADDR": "127.0.0.1:9000", "PROVISION_MODE": "verify", "LOG_LEVEL": "DEBUG",
		"MAX_BODY_BYTES": "2048", "MONGO_OP_TIMEOUT": "500ms", "SHUTDOWN_TIMEOUT": "0s",
		"PASSWORD_HASH_CONCURRENCY": "3", "TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.7,fd00::/8", "WORKER_CONCURRENCY": "4",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.HTTPAddr != "127.0.0.1:9000" || s.ProvisionMode != ProvisionVerify || s.LogLevel != slog.LevelDebug || s.MaxBodyBytes != 2048 ||
		s.MongoOpTimeout != 500*time.Millisecond || s.ShutdownTimeout != 0 || s.PasswordHashConcurrency != 3 || s.WorkerConcurrency != 4 ||
		fmt.Sprint(s.TrustedProxies) != "[10.0.0.0/8 192.168.1.7/32 fd00::/8]" {
		t.Errorf("overrides not applied: %+v", s)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"PROVISION_MODE": "drop", "LOG_LEVEL": "loud", "MAX_BODY_BYTES": "0",
		"MONGO_OP_TIMEOUT": "0s", "SHUTDOWN_TIMEOUT": "soon", "PASSWORD_HASH_CONCURRENCY": "0", "TRUSTED_PROXIES": "10.0.0.0/8,proxy", "WORKER_CONCURRENCY": "0"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"CONFIG_DIR is required", "MONGO_URI is required", "PROVISION_MODE", "LOG_LEVEL", "MAX_BODY_BYTES", "MONGO_OP_TIMEOUT", "SHUTDOWN_TIMEOUT", "PASSWORD_HASH_CONCURRENCY", `TRUSTED_PROXIES: "proxy"`, "WORKER_CONCURRENCY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadSecretsKey(t *testing.T) {
	base := map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m"}
	longKey := strings.Repeat("k", 32)

	t.Run("unset", func(t *testing.T) {
		s, err := Load(env(base))
		if err != nil || s.SecretsKey != nil {
			t.Fatalf("s.SecretsKey = %q, err = %v", s.SecretsKey, err)
		}
	})

	t.Run("raw", func(t *testing.T) {
		m := maps(base, map[string]string{"BACKD_SECRETS_KEY": longKey})
		s, err := Load(env(m))
		if err != nil || string(s.SecretsKey) != longKey {
			t.Fatalf("s.SecretsKey = %q, err = %v", s.SecretsKey, err)
		}
	})

	t.Run("too short", func(t *testing.T) {
		m := maps(base, map[string]string{"BACKD_SECRETS_KEY": "short"})
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "BACKD_SECRETS_KEY must be at least 32 characters") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "key")
		if err := os.WriteFile(path, []byte(longKey+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := maps(base, map[string]string{"BACKD_SECRETS_KEY_FILE": path})
		s, err := Load(env(m))
		if err != nil || string(s.SecretsKey) != longKey {
			t.Fatalf("s.SecretsKey = %q, err = %v", s.SecretsKey, err)
		}
	})

	t.Run("file missing", func(t *testing.T) {
		m := maps(base, map[string]string{"BACKD_SECRETS_KEY_FILE": filepath.Join(t.TempDir(), "missing")})
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "BACKD_SECRETS_KEY_FILE") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("both set", func(t *testing.T) {
		m := maps(base, map[string]string{"BACKD_SECRETS_KEY": longKey, "BACKD_SECRETS_KEY_FILE": "/whatever"})
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "BACKD_SECRETS_KEY and BACKD_SECRETS_KEY_FILE can't both be set") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLoadDev(t *testing.T) {
	base := map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m"}

	t.Run("unset", func(t *testing.T) {
		s, err := Load(env(base))
		if err != nil || s.Dev {
			t.Fatalf("s.Dev = %v, err = %v", s.Dev, err)
		}
	})

	t.Run("true", func(t *testing.T) {
		s, err := Load(env(maps(base, map[string]string{"BACKD_DEV": "true", "DENO": "/opt/deno"})))
		if err != nil || !s.Dev || s.Deno != "/opt/deno" {
			t.Fatalf("s.Dev = %v, s.Deno = %q, err = %v", s.Dev, s.Deno, err)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		_, err := Load(env(maps(base, map[string]string{"BACKD_DEV": "yes"})))
		if err == nil || !strings.Contains(err.Error(), `BACKD_DEV must be true or false, got "yes"`) {
			t.Fatalf("err = %v", err)
		}
	})
}

// maps merges b into a copy of a.
func maps(a, b map[string]string) map[string]string {
	m := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func TestLoadMetrics(t *testing.T) {
	base := map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m"}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return env(m)
	}
	token := strings.Repeat("t", 32)
	if s, err := Load(with()); err != nil || s.MetricsAddr != "" {
		t.Errorf("default: %+v, %v", s, err)
	}
	if s, err := Load(with("METRICS_ADDR", ":9090", "METRICS_TOKEN", token)); err != nil || s.MetricsAddr != ":9090" || s.MetricsToken != token {
		t.Errorf("on: %+v, %v", s, err)
	}
	for name, e := range map[string]func(string) string{
		"token alone":    with("METRICS_TOKEN", token),
		"no port":        with("METRICS_ADDR", "localhost"),
		"port 0":         with("METRICS_ADDR", ":0"),
		"the public one": with("METRICS_ADDR", ":8080"),
		"the internal":   with("METRICS_ADDR", ":8081"),
		"short token":    with("METRICS_ADDR", ":9090", "METRICS_TOKEN", "short"),
	} {
		if _, err := Load(e); err == nil || !strings.Contains(err.Error(), "METRICS_") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAdminSwitches(t *testing.T) {
	base := map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m"}
	load := func(extra map[string]string) (Settings, error) { return Load(env(maps(base, extra))) }

	// Defaults: the admin API on, the UI off.
	s, err := load(nil)
	if err != nil || s.DisableAdminAPI || s.AdminUI {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	for _, tc := range []struct {
		env     map[string]string
		api, ui bool // want DisableAdminAPI, AdminUI
	}{
		{map[string]string{"BACKD_ADMIN_API": "false"}, true, false},
		{map[string]string{"BACKD_ADMIN_API": "TRUE"}, false, false},
		{map[string]string{"BACKD_ADMIN_UI": "true"}, false, true},
		{map[string]string{"BACKD_ADMIN_UI": "true", "BACKD_ADMIN_API": "true"}, false, true},
		{map[string]string{"BACKD_ADMIN_API": "false", "BACKD_ADMIN_UI": "false"}, true, false},
	} {
		s, err := load(tc.env)
		if err != nil || s.DisableAdminAPI != tc.api || s.AdminUI != tc.ui {
			t.Errorf("%v: DisableAdminAPI=%v AdminUI=%v err=%v", tc.env, s.DisableAdminAPI, s.AdminUI, err)
		}
	}
	// The idle timeout: 30 minutes unless set, and a positive duration.
	if s.AdminUIIdle != 30*time.Minute {
		t.Errorf("default idle = %v", s.AdminUIIdle)
	}
	if s, err := load(map[string]string{"BACKD_ADMIN_UI_IDLE": "5m"}); err != nil || s.AdminUIIdle != 5*time.Minute {
		t.Errorf("idle 5m: %v %v", s.AdminUIIdle, err)
	}
	for _, v := range []string{"0", "soon", "-1m"} {
		if _, err := load(map[string]string{"BACKD_ADMIN_UI_IDLE": v}); err == nil || !strings.Contains(err.Error(), "BACKD_ADMIN_UI_IDLE") {
			t.Errorf("idle %q: %v", v, err)
		}
	}
	// The UI is served by an instance that has the admin API.
	if _, err := load(map[string]string{"BACKD_ADMIN_UI": "true", "BACKD_ADMIN_API": "false"}); err == nil || !strings.Contains(err.Error(), "BACKD_ADMIN_UI=true needs the admin API") {
		t.Errorf("UI without the admin API: %v", err)
	}
	for _, name := range []string{"BACKD_ADMIN_API", "BACKD_ADMIN_UI"} {
		if _, err := load(map[string]string{name: "maybe"}); err == nil || !strings.Contains(err.Error(), name+" must be true or false") {
			t.Errorf("%s=maybe: %v", name, err)
		}
	}
}

func TestMaxUploadBytes(t *testing.T) {
	s, err := Load(env(map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m", "BACKD_MAX_UPLOAD_BYTES": "5000"}))
	if err != nil || s.MaxUploadBytes != 5000 {
		t.Errorf("set: %v %d", err, s.MaxUploadBytes)
	}
	if _, err := Load(env(map[string]string{"CONFIG_DIR": "/cfg", "MONGO_URI": "mongodb://m", "BACKD_MAX_UPLOAD_BYTES": "lots"})); err == nil || !strings.Contains(err.Error(), "BACKD_MAX_UPLOAD_BYTES") {
		t.Errorf("bad value: %v", err)
	}
}

func TestImageMaxPixels(t *testing.T) {
	if n, err := ImageMaxPixels(env(nil)); err != nil || n != 40_000_000 {
		t.Errorf("default = %d, %v", n, err)
	}
	if n, err := ImageMaxPixels(env(map[string]string{"BACKD_IMAGE_MAX_PIXELS": "1000000"})); err != nil || n != 1_000_000 {
		t.Errorf("set = %d, %v", n, err)
	}
	for _, bad := range []string{"0", "-5", "many", "1.5"} {
		if _, err := ImageMaxPixels(env(map[string]string{"BACKD_IMAGE_MAX_PIXELS": bad})); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := Load(env(map[string]string{"CONFIG_DIR": "/c", "MONGO_URI": "m", "BACKD_IMAGE_MAX_PIXELS": "x"})); err == nil {
		t.Error("Load accepted a bad BACKD_IMAGE_MAX_PIXELS")
	}
}
