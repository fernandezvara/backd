package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func helpOf(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	// An environment that would make a server fail at once: the help must not look at it.
	env := map[string]string{"CONFIG_DIR": "/nonexistent", "MONGO_URI": "mongodb://localhost:1"}
	code := Run(args, func(k string) string { return env[k] }, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestWithoutACommandBackdPrintsTheGroupedHelp(t *testing.T) {
	code, none, errOut := helpOf(t)
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, title := range []string{"Project (local: CONFIG_DIR only, no running server):", "Account (your sign-in on a running backd):", "Administration (a running backd's admin API):", "General:"} {
		if !strings.Contains(none, "\n"+title+"\n") {
			t.Errorf("the help lacks the group %q:\n%s", title, none)
		}
	}
	if !strings.HasPrefix(none, "Usage: backd <command> [options]\n") || !strings.Contains(none, "`backd <command> --help` shows a command's help.") || !strings.Contains(none, "backd-server --help") || strings.Contains(none, "\n  serve ") {
		t.Errorf("the help:\n%s", none)
	}
	// backd help, backd --help and backd -h are the same page.
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		if code, got, _ := helpOf(t, args...); code != 0 || got != none {
			t.Errorf("%v: exit %d, same=%v", args, code, got == none)
		}
	}
	// A command's own help is still the library's.
	if code, got, errOut := helpOf(t, "help", "user"); code != 0 || !strings.Contains(got+errOut, "manage a realm's users") {
		t.Errorf("help user: %d %q %q", code, got, errOut)
	}
}

// The groups and the command tree cannot drift: every command is in exactly one group,
// and every entry of a group is a command that exists.
func TestEveryCommandIsListedOnceInTheHelp(t *testing.T) {
	getenv := func(string) string { return "" }
	tree := commandSummaries(getenv)
	delete(tree, "help") // listed by hand: the tree doesn't register it
	if len(tree) < 15 {
		t.Fatalf("the command tree was not read: %v", tree)
	}
	seen := map[string]int{}
	for _, g := range helpGroups {
		for _, name := range g.commands {
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
	// And the page shows each once, with its description.
	page := groupedHelp(getenv)
	for name, short := range tree {
		if short == "" {
			t.Errorf("%s has no description", name)
		}
		if n := len(regexp.MustCompile(`(?m)^  `+regexp.QuoteMeta(name)+` +`+regexp.QuoteMeta(short)+`$`).FindAllString(page, -1)); n != 1 {
			t.Errorf("%s is on the page %d times with its description", name, n)
		}
	}
}

// Each command's help says whether it needs a running server; functions, a group of both
// kinds, marks each subcommand.
func TestCommandHelpSaysWhetherItNeedsAServer(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"config", "check", "--help"}, "Server: none needed"},
		{[]string{"template", "help"}, "Server: none needed"},
		{[]string{"functions", "types", "--help"}, "Server: none needed"},
		{[]string{"user", "list", "--help"}, "Server: needs a running backd"},
		{[]string{"login", "--help"}, "Server: needs a running backd"},
		{[]string{"functions", "invoke", "--help"}, "Server: needs a running backd"},
		{[]string{"functions", "--help"}, "`types` is local"},
	} {
		code, out, errOut := helpOf(t, tt.args...)
		if code != 0 || !strings.Contains(out, tt.want) {
			t.Errorf("%v: %d %q %q, want %q", tt.args, code, out, errOut, tt.want)
		}
	}
	_, fns, _ := helpOf(t, "functions", "--help")
	if !regexp.MustCompile(`types +print JSDoc types[^\n]*\[local\]`).MatchString(fns) || !regexp.MustCompile(`invoke +call the function[^\n]*\[server\]`).MatchString(fns) {
		t.Errorf("functions --help doesn't mark its subcommands:\n%s", fns)
	}
	// A command line that only looks like a help request is run, not annotated.
	if _, out, _ := helpOf(t, "version"); strings.Contains(out, "Server:") {
		t.Errorf("version: %q", out)
	}
}
