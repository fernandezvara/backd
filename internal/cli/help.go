package cli

import (
	"io"
	"strings"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// helpGroups is the global help: commands grouped by what they work on, as the packages
// internal/cli/project, account and admin hold them. Every command of the tree appears in
// exactly one group (a test holds the two together); the descriptions are the commands' own
// short help. functions is a group of both kinds: its types subcommand is local, the others
// need a server, and its own help says which.
var helpGroups = []cmdutil.HelpGroup{
	{Title: "Project", Note: "local: CONFIG_DIR only, no running server", Commands: []string{"template", "config", "rules", "databases", "types", "openapi", "export-decrypt"}},
	{Title: "Account", Note: "your sign-in on a running backd", Commands: []string{"login", "logout", "whoami", "account"}},
	{Title: "Administration", Note: "a running backd's admin API", Commands: []string{"user", "orgs", "apikey", "push", "secret", "encrypt", "audit", "data", "storage", "files", "functions", "export", "import", "compliance", "consents", "app-config", "triggers"}},
	{Title: "General", Note: "", Commands: []string{"help", "version"}},
}

// localCommands need no running server; every other command does. A command of a group is
// "<group> <name>" (functions types).
var localCommands = map[string]bool{
	"template": true, "config": true, "rules": true, "databases": true, "types": true, "openapi": true,
	"export-decrypt": true, "functions types": true, "version": true, "help": true,
}

// serverNote is the line a command's help ends with: whether it needs a running backd.
func serverNote(path []string) string {
	if len(path) == 0 {
		return ""
	}
	if path[0] == "functions" && len(path) == 1 {
		return "Server: `types` is local (CONFIG_DIR only); the other subcommands need a running backd."
	}
	key := path[0]
	if len(path) > 1 && path[0] == "functions" {
		key += " " + path[1]
	}
	if localCommands[key] {
		return "Server: none needed, this command works on CONFIG_DIR (or its own input) only."
	}
	return "Server: needs a running backd (--url, else BACKD_URL, else the last one logged in to)."
}

// commandHelpPath is the command a help request is for (`backd user list --help`, `backd user
// help`), or nil when the arguments don't ask for a command's help.
func commandHelpPath(args []string) []string {
	asks := false
	var path []string
	for _, a := range args {
		switch {
		case a == "--help" || a == "-h" || a == "help":
			asks = true
		case strings.HasPrefix(a, "-"):
			return nil // a flag before the help: a command line, not a help request
		default:
			path = append(path, a)
		}
	}
	if !asks {
		return nil
	}
	return path
}

// isGlobalHelp reports whether the arguments ask for the global help: none at all (plain
// `backd` starts nothing), or `help` / `--help` / `-h` alone.
func isGlobalHelp(args []string) bool { return cmdutil.IsGlobalHelp(args) }

// commandSummaries reads the one-line description of every command from the tree's own flat help.
func commandSummaries(getenv func(string) string) map[string]string {
	return cmdutil.Summaries(newCLI(getenv, strings.NewReader(""), io.Discard, io.Discard), "backd")
}

// groupedHelp renders the global help.
func groupedHelp(getenv func(string) string) string {
	return cmdutil.GroupedHelp("backd", helpGroups, commandSummaries(getenv),
		"`backd <command> --help` shows a command's help. The server and the tools that work on\nMongoDB directly are backd-server's (`backd-server --help`).\n")
}
