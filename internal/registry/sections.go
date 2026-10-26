package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// decodeSection reads into doc the part of a collection.yaml that one loader owns. A mistake
// in another section is reported by the loader that reads that section, so it only counts
// here when the error mentions one of own's words; then ok is false and err says it. ok is
// also false, with no error, when the section can't be read and the loader has nothing to do.
func decodeSection(data []byte, path string, doc any, own ...string) (ok bool, err error) {
	err = yaml.NewDecoder(bytes.NewReader(data)).Decode(doc)
	if err == nil || errors.Is(err, io.EOF) {
		return true, nil
	}
	for _, w := range own {
		if strings.Contains(err.Error(), w) {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}
	return false, nil
}
