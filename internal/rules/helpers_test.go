package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// parseYAML compiles the YAML of a `rules:` section the way the registry hands it over.
func parseYAML(src string, schema Schema) (*Set, []error) {
	var doc map[string]string
	dec := yaml.NewDecoder(bytes.NewReader([]byte(src)))
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, []error{fmt.Errorf("invalid YAML: want a mapping of operation to expression string: %w", err)}
	}
	return Parse(doc, schema)
}
