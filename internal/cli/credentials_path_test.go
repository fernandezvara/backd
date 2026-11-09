package cli

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The credentials file: BACKD_CREDENTIALS, else $XDG_CONFIG_HOME/backd/credentials, else
// <home>/.config/backd/credentials, with the home directory found as the standard library
// does on each system (so Windows, which sets USERPROFILE and not HOME, gets one too).
func TestCredentialsPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := homeVar(runtime.GOOS)
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"named", map[string]string{"BACKD_CREDENTIALS": "/c/creds", "XDG_CONFIG_HOME": "/x", home: "/h"}, "/c/creds"},
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/x", home: "/h"}, filepath.Join("/x", "backd", "credentials")},
		{"home", map[string]string{home: "/h"}, filepath.Join("/h", ".config", "backd", "credentials")},
	} {
		if got, err := credentialsPath(env(tt.env)); err != nil || got != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if _, err := credentialsPath(env(nil)); err == nil {
		t.Error("no home and nothing set: no error")
	}
	for goos, want := range map[string]string{"windows": "USERPROFILE", "linux": "HOME", "darwin": "HOME", "plan9": "home"} {
		if got := userHomeDir(env(map[string]string{want: "/u"}), goos); got != "/u" {
			t.Errorf("%s: home %q from %s", goos, got, want)
		}
	}
}
