// Package e2e holds the end-to-end tests: a server started in the test, on MongoDB (and Deno
// for functions), driven through the CLI and the clients like a user would.
package e2e

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/cli"
	"github.com/fernandezvara/backd/internal/servercli"
)

// cliEnv runs the CLI with an environment the test can change.
type cliEnv struct {
	t   *testing.T
	env map[string]string
}

func (c *cliEnv) run(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runAny(args, func(k string) string { return c.env[k] }, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// runAny runs a command line on the binary that has it: backd-server for what opens MongoDB or
// runs Deno, the backd CLI for the rest (as internal/servercli's tests do).
func runAny(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if isServerCommand(args) {
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--local" })
		return servercli.Run(args, getenv, stdin, stdout, stderr)
	}
	return cli.Run(args, getenv, stdin, stdout, stderr)
}

func isServerCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if len(args) > 1 && args[0] == "user" && args[1] == "mfa-reset" && slices.Contains(args, "--local") {
		return true // the local form, a server tool
	}
	switch args[0] {
	case "serve", "worker", "executor", "egress", "provision", "bootstrap":
		return true
	}
	if len(args) > 1 {
		switch args[0] + " " + args[1] {
		case "functions build", "secret rotate-key", "triggers rescan":
			return true
		}
	}
	return false
}

// expect runs the CLI and checks the exit code and that stdout or stderr
// contains want.
func (c *cliEnv) expect(wantCode int, want, stdin string, args ...string) string {
	c.t.Helper()
	code, out, errOut := c.run(stdin, args...)
	if code != wantCode || !strings.Contains(out+errOut, want) {
		c.t.Errorf("%v: code %d, stdout %q, stderr %q; want %d and %q", args, code, out, errOut, wantCode, want)
	}
	return out
}
