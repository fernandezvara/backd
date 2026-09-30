package settings

import (
	"strings"
	"testing"
)

func TestExecutorSettings(t *testing.T) {
	load := func(env map[string]string) (Settings, error) {
		return Load(func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return map[string]string{"CONFIG_DIR": "/c", "MONGO_URI": "mongodb://m"}[k]
		})
	}
	s, err := load(nil)
	if err != nil || s.ExecutorURL != "" || s.InternalAddr != ":8081" {
		t.Errorf("defaults: %+v %v", s, err)
	}
	secret := strings.Repeat("s", 32)
	s, err = load(map[string]string{"BACKD_EXECUTOR_URL": "http://executor:9100", "BACKD_EXECUTOR_TOKEN": secret,
		"BACKD_CALLBACK_URL": "http://backd:8081", "BACKD_CALLBACK_KEY": secret, "BACKD_INTERNAL_ADDR": ":9999"})
	if err != nil || s.ExecutorURL != "http://executor:9100" || s.CallbackURL != "http://backd:8081" || s.InternalAddr != ":9999" {
		t.Errorf("configured: %+v %v", s, err)
	}
	_, err = load(map[string]string{"BACKD_EXECUTOR_URL": "executor:9100", "BACKD_EXECUTOR_TOKEN": "short", "BACKD_CALLBACK_KEY": "short"})
	for _, want := range []string{"BACKD_EXECUTOR_URL must be an http(s) URL", "BACKD_EXECUTOR_TOKEN must be at least 32", "BACKD_CALLBACK_URL must be", "BACKD_CALLBACK_KEY must be at least 32"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}
