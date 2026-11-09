// Package cli is backd's command tree: every command, run in-process by
// Run so tests drive it like the binary.
package cli

import (
	"fmt"
	"io"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// Run executes one backd command line and returns the process exit code.
func Run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	if isGlobalHelp(args) {
		fmt.Fprint(stdout, groupedHelp(getenv))
		return 0
	}
	code := cmdutil.Execute(newCLI(getenv, stdin, stdout, stderr), "backd", args, stderr)
	// A command's help ends with whether it needs a running server.
	if path := commandHelpPath(args); code == 0 && len(path) > 0 {
		fmt.Fprintf(stdout, "\n%s\n", serverNote(path))
	}
	return code
}
