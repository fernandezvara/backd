package authcore

import (
	"os/exec"
	"strings"
	"testing"
)

// The packages the CLI shares with the server must not link the server: no
// MongoDB driver, change feed, realtime hub or HTTP API.
func TestSharedPackagesAreDriverFree(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	shared := []string{"authcore", "settings/limits", "apiheaders", "cmdutil"}
	banned := []string{"go.mongodb.org/", "internal/mongodb", "internal/changefeed", "internal/realtime", "internal/httpapi"}
	for _, p := range shared {
		out, err := exec.Command(goBin, "list", "-deps", "github.com/fernandezvara/backd/internal/"+p).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", p, err)
		}
		for dep := range strings.Lines(string(out)) {
			for _, b := range banned {
				if strings.Contains(dep, b) {
					t.Errorf("internal/%s depends on %s", p, strings.TrimSpace(dep))
				}
			}
		}
	}
}
