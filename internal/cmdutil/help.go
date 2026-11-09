package cmdutil

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/fernandezvara/kli"
)

// HelpGroup is a section of a binary's global help: the commands that belong together and a
// note on what they have in common.
type HelpGroup struct {
	Title, Note string
	Commands    []string
}

// IsGlobalHelp reports whether the arguments ask for the global help: none at all (a bare
// binary starts nothing), or `help` / `--help` / `-h` alone.
func IsGlobalHelp(args []string) bool {
	if len(args) == 0 {
		return true
	}
	return len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")
}

// Summaries reads the one-line description of every command from the tree's own flat help,
// so the grouped help cannot say something else than `<prog> <command> --help`. The tree
// doesn't register help itself, so it is added.
func Summaries(cfg *kli.Config, prog string) map[string]string {
	var flat bytes.Buffer
	cfg.SetIO(strings.NewReader(""), &flat, io.Discard)
	_ = cfg.Execute([]string{prog, "--help"})
	out := map[string]string{"help": "show this help, or a command's help: " + prog + " help <command>"}
	line := regexp.MustCompile(`^  (\S+)\s+(\S.*)$`)
	for l := range strings.SplitSeq(flat.String(), "\n") {
		if m := line.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// GroupedHelp renders the global help of prog: the groups in order, then the footer.
func GroupedHelp(prog string, groups []HelpGroup, summaries map[string]string, footer string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: %s <command> [options]\n", prog)
	for _, g := range groups {
		if g.Note != "" {
			fmt.Fprintf(&b, "\n%s (%s):\n", g.Title, g.Note)
		} else {
			fmt.Fprintf(&b, "\n%s:\n", g.Title)
		}
		for _, name := range g.Commands {
			fmt.Fprintf(&b, "  %-15s %s\n", name, summaries[name])
		}
	}
	b.WriteString("\n" + footer)
	return b.String()
}
