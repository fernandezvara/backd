package cli

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// helpGroups is the global help: commands grouped by what they work on, as the packages
// internal/cli/project, account and admin hold them. Every command of the tree appears in
// exactly one group (a test holds the two together); the descriptions are the commands' own
// short help. functions is a group of both kinds: its types subcommand is local, the others
// need a server, and its own help says which.
var helpGroups = []struct {
	title, note string
	commands    []string
}{
	{"Project", "local: CONFIG_DIR only, no running server", []string{"template", "config", "rules", "databases", "types", "openapi", "export-decrypt"}},
	{"Account", "your sign-in on a running backd", []string{"login", "logout", "whoami", "account"}},
	{"Administration", "a running backd's admin API", []string{"user", "orgs", "apikey", "push", "secret", "encrypt", "audit", "data", "storage", "files", "functions", "export", "import", "compliance", "consents", "app-config", "triggers"}},
	{"General", "", []string{"help", "version"}},
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

// helpCommand is the entry for `help`, which the command tree doesn't register itself.
const helpCommand = "show this help, or a command's help: backd help <command>"

// isGlobalHelp reports whether the arguments ask for the global help: none at all (plain
// `backd` starts nothing), or `help` / `--help` / `-h` alone.
func isGlobalHelp(args []string) bool {
	if len(args) == 0 {
		return true
	}
	return len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")
}

// commandSummaries reads the one-line description of every command from the tree's own
// flat help, so the grouped help cannot say something else than `backd <command> --help`.
func commandSummaries(getenv func(string) string) map[string]string {
	var flat bytes.Buffer
	cfg := newCLI(getenv, strings.NewReader(""), &flat, io.Discard)
	_ = cfg.Execute([]string{"backd", "--help"})
	out := map[string]string{"help": helpCommand}
	line := regexp.MustCompile(`^  (\S+)\s+(\S.*)$`)
	for l := range strings.SplitSeq(flat.String(), "\n") {
		if m := line.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// groupedHelp renders the global help.
func groupedHelp(getenv func(string) string) string {
	summaries := commandSummaries(getenv)
	var b strings.Builder
	b.WriteString("Usage: backd <command> [options]\n")
	for _, g := range helpGroups {
		if g.note != "" {
			fmt.Fprintf(&b, "\n%s (%s):\n", g.title, g.note)
		} else {
			fmt.Fprintf(&b, "\n%s:\n", g.title)
		}
		for _, name := range g.commands {
			fmt.Fprintf(&b, "  %-15s %s\n", name, summaries[name])
		}
	}
	b.WriteString("\n`backd <command> --help` shows a command's help. The server and the tools that work on\nMongoDB directly are backd-server's (`backd-server --help`).\n")
	return b.String()
}
