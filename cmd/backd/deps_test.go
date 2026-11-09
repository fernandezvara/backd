package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The CLI must never link the server: these packages, and the MongoDB driver, open MongoDB or
// serve the API, and belong to backd-server.
var serverPackages = []string{
	"github.com/fernandezvara/backd/internal/mongodb",
	"github.com/fernandezvara/backd/internal/httpapi",
	"github.com/fernandezvara/backd/internal/executor",
	"github.com/fernandezvara/backd/internal/egress",
	"github.com/fernandezvara/backd/internal/changefeed",
	"github.com/fernandezvara/backd/internal/realtime",
	"github.com/fernandezvara/backd/internal/server",
	"github.com/fernandezvara/backd/internal/servercli",
	"go.mongodb.org/mongo-driver",
}

func TestTheCLINeverLinksTheServer(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	graph := map[string][]string{}
	root := ""
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		graph[fields[0]] = fields[1:]
		root = fields[0] // go list -deps prints the package itself last
	}
	for _, chain := range forbiddenChains(graph, root, serverPackages) {
		t.Errorf("the backd CLI links a server package: %s", strings.Join(chain, " -> "))
	}
}

// forbiddenChains finds, for each forbidden package (a prefix) the graph reaches from root,
// the shortest import chain to it.
func forbiddenChains(graph map[string][]string, root string, forbidden []string) [][]string {
	banned := func(p string) bool {
		return slices.ContainsFunc(forbidden, func(f string) bool { return p == f || strings.HasPrefix(p, f+"/") })
	}
	prev := map[string]string{root: ""}
	queue := []string{root}
	var chains [][]string
	found := map[string]bool{}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, next := range graph[p] {
			if _, seen := prev[next]; seen {
				continue
			}
			prev[next] = p
			if banned(next) {
				f := next
				for _, b := range forbidden {
					if next == b || strings.HasPrefix(next, b+"/") {
						f = b
					}
				}
				if !found[f] {
					found[f] = true
					var chain []string
					for at := next; at != ""; at = prev[at] {
						chain = append([]string{at}, chain...)
					}
					chains = append(chains, chain)
				}
				continue
			}
			queue = append(queue, next)
		}
	}
	return chains
}

func TestForbiddenChainsNamesTheImportChain(t *testing.T) {
	graph := map[string][]string{
		"cmd":                {"internal/cli", "fmt"},
		"internal/cli":       {"internal/cli/admin"},
		"internal/cli/admin": {"internal/server", "internal/x"},
		"internal/server":    {"go.mongodb.org/mongo-driver/v2/mongo"},
		"internal/x":         {"go.mongodb.org/mongo-driver/v2/bson"},
		"fmt":                nil,
	}
	got := forbiddenChains(graph, "cmd", []string{"internal/server", "go.mongodb.org/mongo-driver"})
	want := [][]string{
		{"cmd", "internal/cli", "internal/cli/admin", "internal/server"},
		{"cmd", "internal/cli", "internal/cli/admin", "internal/x", "go.mongodb.org/mongo-driver/v2/bson"},
	}
	if len(got) != len(want) {
		t.Fatalf("chains = %v", got)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("chain %d = %v, want %v", i, got[i], want[i])
		}
	}
}
