package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUserArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no command", []string{"user"}, 0, "Usage: backd user"},
		{"help", []string{"user", "help"}, 0, "Usage: backd user"},
		{"unknown command", []string{"user", "rename", "--realm", "r", "--email", "a@x.io"}, 2, `unknown command "rename"`},
		{"missing email", []string{"user", "create", "--realm", "r"}, 2, "--email string (required) -> Not provided"},
		{"extra argument", []string{"user", "list", "--realm", "r", "x"}, 2, "unexpected arguments: [x]"},
		{"unknown flag", []string{"user", "create", "--realm", "r", "--email", "a@x.io", "--admin"}, 2, "flag provided but not defined: -admin"},
		{"flag of another command", []string{"user", "list", "--realm", "r", "--yes"}, 2, "flag provided but not defined: -yes"},
		{"no server", []string{"user", "list", "--realm", "r"}, 1, "no server given"},
		{"url without value", []string{"user", "list", "--realm", "r", "--url"}, 2, "flag needs an argument: -url"},
		{"bad url", []string{"user", "list", "--realm", "r", "--url", "backd.example.com"}, 1, "invalid server URL"},
		{"networks flag on create", []string{"user", "create", "--realm", "r", "--email", "a@x.io", "--login", "10.0.0.0/8"}, 2, "flag provided but not defined: -login"},
		{"old role syntax", []string{"user", "role", "add"}, 2, `unknown command "role"`},
		{"role without role", []string{"user", "add-role", "--realm", "r", "--email", "a@x.io"}, 2, "--role string (required) -> Not provided"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			home := t.TempDir()
			getenv := func(k string) string {
				if k == "HOME" {
					return home
				}
				return ""
			}
			code := Run(tt.args, getenv, strings.NewReader(""), &stderr, &stderr)
			if code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("code %d, stderr %q; want %d and %q", code, stderr.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestRunAPIKeyArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no command", []string{"apikey"}, 0, "Usage: backd apikey"},
		{"help", []string{"apikey", "help"}, 0, "Usage: backd apikey"},
		{"unknown command", []string{"apikey", "rotate", "--realm", "r", "k"}, 2, `unknown command "rotate"`},
		{"missing name", []string{"apikey", "create", "--realm", "r"}, 2, "--name string (required) -> Not provided"},
		{"expires without value", []string{"apikey", "create", "--realm", "r", "--name", "k", "--expires"}, 2, "flag needs an argument: -expires"},
		{"role without value", []string{"apikey", "create", "--realm", "r", "--name", "k", "--role"}, 2, "flag needs an argument: -role"},
		{"bad role", []string{"apikey", "create", "--realm", "r", "--name", "k", "--role", "root"}, 2, "not one of"},
		{"role on list", []string{"apikey", "list", "--realm", "r", "--role", "admin"}, 2, "flag provided but not defined: -role"},
		{"bad networks", []string{"apikey", "create", "--realm", "r", "--name", "k", "--networks", "10.0.0.0/8,office"}, 2, "--networks: want comma-separated"},
		{"bad expires", []string{"apikey", "create", "--realm", "r", "--name", "k", "--expires", "soon"}, 2, "want a positive duration"},
		{"expires on list", []string{"apikey", "list", "--realm", "r", "--expires", "1d"}, 2, "flag provided but not defined: -expires"},
		{"no server", []string{"apikey", "list", "--realm", "r"}, 1, "no server given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			home := t.TempDir()
			getenv := func(k string) string {
				if k == "HOME" {
					return home
				}
				return ""
			}
			code := Run(tt.args, getenv, strings.NewReader(""), &stderr, &stderr)
			if code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("code %d, stderr %q; want %d and %q", code, stderr.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}
