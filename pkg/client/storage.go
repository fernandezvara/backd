package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// TokenStorage keeps the session token between calls (and, for a file, between runs). An empty
// token means signed out.
type TokenStorage interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, token string) error
	Remove(ctx context.Context) error
}

// SlotStorage is a storage that can keep a token per organization, which [Client.Org] needs.
type SlotStorage interface {
	TokenStorage
	// Slot returns the storage of one organization.
	Slot(name string) TokenStorage
	// Slots lists the organizations that hold a token.
	Slots(ctx context.Context) ([]string, error)
}

// MemoryStorage keeps the token in memory. It is the default and is safe for concurrent use.
type MemoryStorage struct {
	mu    sync.Mutex
	token string
	slots map[string]*MemoryStorage
}

// NewMemoryStorage returns an empty in-memory storage.
func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{} }

func (m *MemoryStorage) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, nil
}

func (m *MemoryStorage) Set(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = token
	return nil
}

func (m *MemoryStorage) Remove(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = ""
	return nil
}

func (m *MemoryStorage) Slot(name string) TokenStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slots == nil {
		m.slots = map[string]*MemoryStorage{}
	}
	s := m.slots[name]
	if s == nil {
		s = NewMemoryStorage()
		m.slots[name] = s
	}
	return s
}

func (m *MemoryStorage) Slots(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name, s := range m.slots {
		if t, _ := s.Get(context.Background()); t != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// FileStorage keeps the token in a file (mode 0600), so a script or a tool stays signed in
// between runs. Slots are files next to it, named <path>.<organization>.
type FileStorage struct {
	path string
	mu   sync.Mutex
}

// NewFileStorage returns a storage that keeps the token in path.
func NewFileStorage(path string) *FileStorage { return &FileStorage{path: path} }

func (f *FileStorage) Get(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func (f *FileStorage) Set(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *FileStorage) Remove(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := os.Remove(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (f *FileStorage) Slot(name string) TokenStorage { return NewFileStorage(f.path + "." + name) }

func (f *FileStorage) Slots(context.Context) ([]string, error) {
	matches, err := filepath.Glob(f.path + ".*")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range matches {
		name := strings.TrimPrefix(m, f.path+".")
		if name != "" && !strings.HasSuffix(name, ".tmp") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
