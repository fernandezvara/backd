package servercli

import (
	"io"
	"strings"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// helpGroups is the global help: the long-running roles first, then the commands that run once
// and exit. Every command of the tree appears in exactly one group (a test holds the two
// together); the descriptions are the commands' own short help.
var helpGroups = []cmdutil.HelpGroup{
	{Title: "Daemons", Note: "long-running roles: containers, pods, a service manager", Commands: []string{"serve", "worker", "executor", "egress"}},
	{Title: "Server commands", Note: "run once and exit, on MongoDB and CONFIG_DIR or with Deno", Commands: []string{"provision", "functions", "triggers", "bootstrap", "secret", "user", "export", "import"}},
	{Title: "General", Commands: []string{"help", "version"}},
}

// commandSummaries reads the one-line description of every command from the tree's own flat help.
func commandSummaries(getenv func(string) string) map[string]string {
	return cmdutil.Summaries(newCLI(getenv, strings.NewReader(""), io.Discard, io.Discard), "backd-server")
}

// groupedHelp renders the global help.
func groupedHelp(getenv func(string) string) string {
	return cmdutil.GroupedHelp("backd-server", helpGroups, commandSummaries(getenv),
		"`backd-server <command> --help` shows a command's help. The developer's and administrator's\ncommands (templates, checks, users, keys, data) are the backd CLI's (`backd --help`).\n")
}
