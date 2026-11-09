// Package e2e holds the end-to-end tests: a server started in the test, on MongoDB (and Deno
// for functions), driven through the CLI and the clients like a user would.
package e2e

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/cli"
)

// cliEnv runs the CLI with an environment the test can change.
type cliEnv struct {
	t   *testing.T
	env map[string]string
}

func (c *cliEnv) run(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, func(k string) string { return c.env[k] }, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
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
