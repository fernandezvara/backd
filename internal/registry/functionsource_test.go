package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contents a response carries are bounded: one file is cut, and past the total the rest
// come without content; the files are all listed either way.
func TestReadSourceLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("big/index.ts", strings.Repeat("é", MaxSourceFileBytes)) // two bytes a character: cut in the middle of one
	for i := 0; i < 9; i++ {
		write("big/part"+string(rune('a'+i))+".ts", strings.Repeat("x", MaxSourceFileBytes))
	}
	fns := &Functions{Dir: dir}
	src, err := fns.ReadSource(&Function{Database: "app", Name: "big", Entry: "big/index.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Files) != 10 || src.Files[0].Path != "big/index.ts" || src.Files[0].Size != 2*MaxSourceFileBytes {
		t.Fatalf("files: %+v", src.Files)
	}
	first := src.Files[0]
	if !first.Truncated || first.Content == nil || len(*first.Content) > MaxSourceFileBytes || len(*first.Content) < MaxSourceFileBytes-1 {
		t.Errorf("a big file is cut: truncated=%v len=%d", first.Truncated, len(deref(first.Content)))
	}
	spent, bare := 0, 0
	for _, f := range src.Files {
		if f.Content != nil {
			spent += len(*f.Content)
		} else {
			bare++
			if !f.Truncated {
				t.Errorf("%s has no content but is not marked truncated", f.Path)
			}
		}
	}
	if spent > MaxSourceTotalBytes || bare == 0 {
		t.Errorf("total content %d (limit %d), files without content %d", spent, MaxSourceTotalBytes, bare)
	}
	if src.Bundle != nil {
		t.Errorf("nothing was built: %+v", src.Bundle)
	}
}
