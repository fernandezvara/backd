package servercli

import (
	"regexp"
	"strings"
	"testing"
)

func TestGlobalHelpIsGrouped(t *testing.T) {
	var out, errOut strings.Builder
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		out.Reset()
		if code := Run(args, func(string) string { return "" }, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		for _, want := range []string{"Usage: backd-server <command> [options]", "Daemons (", "Server commands (", "General:", "backd --help"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: the help lacks %q:\n%s", args, want, out.String())
			}
		}
	}
}

// The groups and the command tree cannot drift: every command is in exactly one group, and
// every entry of a group is a command that exists.
func TestEveryServerCommandIsListedOnceInTheHelp(t *testing.T) {
	getenv := func(string) string { return "" }
	tree := commandSummaries(getenv)
	delete(tree, "help") // listed by hand: the tree doesn't register it
	if len(tree) < 10 {
		t.Fatalf("the command tree was not read: %v", tree)
	}
	seen := map[string]int{}
	for _, g := range helpGroups {
		for _, name := range g.Commands {
			seen[name]++
		}
	}
	for name := range tree {
		if seen[name] != 1 {
			t.Errorf("command %q is in %d groups of the help (want 1)", name, seen[name])
		}
	}
	for name, n := range seen {
		if _, ok := tree[name]; !ok && name != "help" {
			t.Errorf("the help lists %q, which is not a command", name)
		}
		if n > 1 {
			t.Errorf("%q is listed %d times", name, n)
		}
	}
	page := groupedHelp(getenv)
	for name, short := range tree {
		if n := len(regexp.MustCompile(`(?m)^  `+regexp.QuoteMeta(name)+` +`+regexp.QuoteMeta(short)+`$`).FindAllString(page, -1)); n != 1 {
			t.Errorf("%s is on the page %d times with its description", name, n)
		}
	}
}
