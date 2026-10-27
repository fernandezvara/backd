package exportpkg

import (
	"io"
	"os"
	"path/filepath"
)

// WriteAtomic writes dst through fn: the bytes go to dst+".partial", which replaces dst only
// when fn and the close succeed, and is removed otherwise. The returned Hasher holds the
// checksum and size of what fn wrote.
func WriteAtomic(dst string, fn func(w io.Writer) error) (*Hasher, error) {
	partial := dst + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	h := NewHasher()
	err = fn(io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, dst)
	}
	if err != nil {
		_ = os.Remove(partial)
		return nil, err
	}
	return h, nil
}

// Write stores the manifest as ManifestName in dir.
func (m *Manifest) Write(dir string) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), data, 0o600)
}
