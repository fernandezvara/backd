package sso_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The library knows nothing of backd (design/sso.md §3.1): nothing under sso/ may import a
// package of this module outside sso/, so it can move to a module of its own unchanged.
func TestLibraryImportsNothingOfBackd(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-test", "-f", "{{.ImportPath}}", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	const root = "github.com/fernandezvara/backd"
	for _, pkg := range strings.Fields(string(out)) {
		if !strings.HasPrefix(pkg, root) || strings.HasPrefix(pkg, root+"/sso") {
			continue // outside the module, or the library's own packages and their test binaries
		}
		t.Errorf("sso depends on %s", pkg)
	}
}
