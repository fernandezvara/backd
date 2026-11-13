package passkey

import (
	"os/exec"
	"strings"
	"testing"
)

// The package is a leaf (design/passkeys.md §14): it must not reach into the rest of backd or into
// the database driver. Everything it needs is passed in.
func TestPasskeyIsALeaf(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, banned := range []string{
			"github.com/fernandezvara/backd/internal/auth", // but not this package itself or its test helpers
			"github.com/fernandezvara/backd/internal/mongodb",
			"github.com/fernandezvara/backd/internal/httpapi",
			"go.mongodb.org/mongo-driver",
		} {
			if (dep == banned || strings.HasPrefix(dep, banned+"/")) && dep != "github.com/fernandezvara/backd/internal/auth/passkey" {
				t.Errorf("passkey imports %s", dep)
			}
		}
	}
}
