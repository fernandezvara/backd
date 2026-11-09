package servercli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/cli"
)

// runAny runs a command line on the binary that has it: backd-server for what opens MongoDB or
// runs Deno, the backd CLI for the rest, so a test can drive both like an operator would. The
// old direct forms (--direct, --local) are backd-server's commands without the flag.
func runAny(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if server, rest := serverCommand(args); server {
		return Run(rest, getenv, stdin, stdout, stderr)
	}
	return cli.Run(args, getenv, stdin, stdout, stderr)
}

func serverCommand(args []string) (bool, []string) {
	var rest []string
	direct := false
	for _, a := range args {
		if a == "--direct" || a == "--local" {
			direct = true
			continue
		}
		rest = append(rest, a)
	}
	if direct || len(rest) == 0 {
		return direct, rest
	}
	switch rest[0] {
	case "serve", "worker", "executor", "egress", "provision", "bootstrap":
		return true, rest
	}
	if len(rest) > 1 {
		switch rest[0] + " " + rest[1] {
		case "functions build", "secret rotate-key", "triggers rescan":
			return true, rest
		}
	}
	return false, rest
}

// cliEnv runs either binary with an environment the test can change.
type cliEnv struct {
	t   *testing.T
	env map[string]string
}

func (c *cliEnv) run(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runAny(args, func(k string) string { return c.env[k] }, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// expect runs a command and checks the exit code and that stdout or stderr contains want.
func (c *cliEnv) expect(wantCode int, want, stdin string, args ...string) string {
	c.t.Helper()
	code, out, errOut := c.run(stdin, args...)
	if code != wantCode || !strings.Contains(out+errOut, want) {
		c.t.Errorf("%v: code %d, stdout %q, stderr %q; want %d and %q", args, code, out, errOut, wantCode, want)
	}
	return out
}

func TestRun(t *testing.T) {
	missingDir := map[string]string{"CONFIG_DIR": "/nonexistent", "MONGO_URI": "mongodb://localhost"}
	empty := t.TempDir()
	for _, tt := range []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		wantOut  string
	}{
		{"invalid settings", []string{"serve"}, nil, 1, "CONFIG_DIR is required"},
		{"invalid config", []string{"serve"}, missingDir, 1, "invalid config"},
		{"provision", []string{"provision"}, missingDir, 1, "backd-server provision:"},
		{"extra arguments", []string{"serve", "x"}, nil, 2, "unexpected arguments"},
		{"serve with-worker parses", []string{"serve", "--with-worker"}, missingDir, 1, "invalid config"},
		{"worker no config", []string{"worker"}, nil, 1, "CONFIG_DIR is required"},
		{"worker extra arguments", []string{"worker", "x"}, nil, 2, "unexpected arguments"},
		{"the CLI's commands are backd's", []string{"login"}, nil, 2, `unknown command: "login"`},
		{"functions build extra", []string{"functions", "build", "extra"}, nil, 2, "unexpected arguments"},
		{"functions build flag", []string{"functions", "build", "--force"}, nil, 2, "flag provided but not defined: -force"},
		{"functions build no config", []string{"functions", "build"}, nil, 1, "CONFIG_DIR is required"},
		{"functions build nothing", []string{"functions", "build"}, map[string]string{"CONFIG_DIR": empty}, 0, "no functions in CONFIG_DIR"},
		{"bootstrap help", []string{"bootstrap", "help"}, nil, 0, "Usage: backd-server bootstrap"},
		{"bootstrap email", []string{"bootstrap", "--realm", "acme"}, nil, 2, "--email"},
		{"bootstrap config", []string{"bootstrap", "--realm", "acme", "--email", "a@x.io"}, nil, 1, "CONFIG_DIR is required"},
		{"serve help", []string{"serve", "--help"}, nil, 0, "Usage: backd-server serve"},
		{"version", []string{"--version"}, nil, 0, "backd-server dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			var out bytes.Buffer
			if code := Run(tt.args, getenv, nil, &out, &out); code != tt.wantCode || !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("%v: code %d, output %q; want %d and %q", tt.args, code, out.String(), tt.wantCode, tt.wantOut)
			}
		})
	}
}
