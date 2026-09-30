package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type indexDecl struct {
	Fields []string `json:"fields"`
	Unique bool     `json:"unique"`
}

// loadIndexes reads the optional indexes.json of a collection.
func loadIndexes(c *Collection, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var decls []indexDecl
	if err := dec.Decode(&decls); err != nil {
		return fmt.Errorf("%s: invalid index declarations (want an array of {\"fields\": [...], \"unique\": bool}): %w", path, err)
	}

	var errs []error
	seen := map[string]int{}
	for i, d := range decls {
		ix, err := parseIndex(c, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: index %d: %w", path, i, err))
			continue
		}
		if j, dup := seen[ix.String()]; dup {
			errs = append(errs, fmt.Errorf("%s: index %d: same fields as index %d", path, i, j))
			continue
		}
		seen[ix.String()] = i
		c.Indexes = append(c.Indexes, ix)
	}
	c.IndexPath = path
	return errors.Join(errs...)
}

func parseIndex(c *Collection, d indexDecl) (Index, error) {
	if len(d.Fields) == 0 {
		return Index{}, errors.New(`"fields" must list at least one field`)
	}
	ix := Index{Unique: d.Unique}
	used := map[string]bool{}
	for _, f := range d.Fields {
		k := IndexKey{Field: f}
		if strings.HasPrefix(f, "-") {
			k = IndexKey{Field: f[1:], Desc: true}
		}
		switch {
		case !c.IsKnownField(k.Field):
			return Index{}, fmt.Errorf("field %q is not declared in schema.json", k.Field)
		case used[k.Field]:
			return Index{}, fmt.Errorf("field %q appears twice", k.Field)
		}
		used[k.Field] = true
		ix.Keys = append(ix.Keys, k)
	}
	return ix, nil
}
