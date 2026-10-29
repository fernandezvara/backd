package client

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func testStorage(t *testing.T, s SlotStorage) {
	t.Helper()
	if tok, err := s.Get(bg); tok != "" || err != nil {
		t.Fatalf("new storage: %q %v", tok, err)
	}
	s.Set(bg, "t1")
	if tok, _ := s.Get(bg); tok != "t1" {
		t.Errorf("Get = %q", tok)
	}
	a := s.Slot("acme")
	a.Set(bg, "ta")
	if tok, _ := s.Get(bg); tok != "t1" {
		t.Errorf("a slot changed the root: %q", tok)
	}
	if names, _ := s.Slots(bg); !slices.Equal(names, []string{"acme"}) {
		t.Errorf("Slots = %v", names)
	}
	a.Remove(bg)
	s.Remove(bg)
	s.Remove(bg) // twice is fine
	if tok, _ := s.Get(bg); tok != "" {
		t.Errorf("after Remove: %q", tok)
	}
	if names, _ := s.Slots(bg); len(names) != 0 {
		t.Errorf("Slots after Remove = %v", names)
	}
}

func TestMemoryStorage(t *testing.T) { testStorage(t, NewMemoryStorage()) }

func TestFileStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "session")
	testStorage(t, NewFileStorage(path))

	s := NewFileStorage(path)
	s.Set(bg, "kept")
	if tok, _ := NewFileStorage(path).Get(bg); tok != "kept" {
		t.Errorf("a new storage on the same file: %q", tok)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v %v", fi.Mode(), err)
	}
}
