package exportpkg

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "part.jsonl")

	h, err := WriteAtomic(dst, func(w io.Writer) error {
		_, err := io.WriteString(w, "hello\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "hello\n" {
		t.Errorf("content = %q", got)
	}
	if h.Size() != 6 || h.Sum() == "" {
		t.Errorf("hasher: size %d, sum %q", h.Size(), h.Sum())
	}
	if _, err := os.Stat(dst + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file left behind: %v", err)
	}

	boom := errors.New("boom")
	if _, err := WriteAtomic(dst, func(w io.Writer) error {
		_, _ = io.WriteString(w, "half")
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "hello\n" {
		t.Errorf("a failed write replaced the file: %q", got)
	}
	if _, err := os.Stat(dst + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file left behind after a failure: %v", err)
	}
}

func TestManifestWrite(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{}
	if err := m.Write(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Fatal(err)
	}
}
