package servercli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The commands that moved to backd-server must not be named as backd's anywhere in the code:
// errors, hints, help and comments all point at the binary that has them.
func TestNoGoCodeNamesAMovedCommandAsBackds(t *testing.T) {
	moved := regexp.MustCompile(`(^|[^-./\w])backd (serve|worker|provision|executor|egress|bootstrap|functions build|secret rotate-key|triggers rescan)\b|--direct\b|mfa-reset --local`)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				if moved.MatchString(line) {
					t.Errorf("%s:%d names a command backd-server has: %s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
