package main

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// helpGroups is the global help: commands grouped by who uses them. Every command of the
// tree appears in exactly one group (a test holds the two together); the descriptions
// are the commands' own short help.
var helpGroups = []struct {
	title    string
	commands []string
}{
	{"Develop", []string{"template", "config", "rules", "databases", "functions"}},
	{"Session", []string{"login", "logout", "whoami"}},
	{"Administer a realm", []string{"bootstrap", "user", "apikey", "secret", "audit", "data", "storage", "files"}},
	{"Server", []string{"serve", "provision", "worker", "executor", "egress"}},
	{"General", []string{"help", "version"}},
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
	for _, l := range strings.Split(flat.String(), "\n") {
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
		fmt.Fprintf(&b, "\n%s:\n", g.title)
		for _, name := range g.commands {
			fmt.Fprintf(&b, "  %-12s %s\n", name, summaries[name])
		}
	}
	b.WriteString("\n`backd <command> --help` shows a command's help.\n")
	return b.String()
}
