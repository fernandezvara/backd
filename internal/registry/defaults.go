package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Default is a value schema.json declares with `default` on a property: a create (and a
// PUT, which replaces the whole document) fills it in when the request doesn't send the
// field.
type Default struct {
	Path  []string // the property names from the document's top, e.g. ["address", "country"]
	Value any
}

// collectDefaults finds the `default` of every property reachable through `properties`
// (not through array items, `allOf`, `$ref` and the like), parents before children and
// siblings by name, and checks that each value satisfies its own property's schema.
func collectDefaults(schemaPath string, raw map[string]any) ([]Default, error) {
	var out []Default
	var errs []string
	var walk func(path []string, node map[string]any)
	walk = func(path []string, node map[string]any) {
		props, _ := node["properties"].(map[string]any)
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ps, _ := props[name].(map[string]any)
			if ps == nil {
				continue
			}
			p := append(append([]string{}, path...), name)
			if v, ok := ps["default"]; ok {
				if err := checkDefault(ps, v); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", strings.Join(p, "."), err))
				} else {
					out = append(out, Default{Path: p, Value: v})
				}
			}
			walk(p, ps)
		}
	}
	walk(nil, raw)
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s: a `default` doesn't satisfy its property's schema: %s", schemaPath, strings.Join(errs, "; "))
	}
	// Parents first, so a default object exists before the defaults inside it.
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Path) < len(out[j].Path) })
	return out, nil
}

// checkDefault validates v against the property's own schema. A property that uses `$ref`
// can't be compiled alone and is left to the check every write goes through.
func checkDefault(prop map[string]any, v any) error {
	if usesRef(prop) {
		return nil
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	const url = "mem://default.json"
	if err := c.AddResource(url, prop); err != nil {
		return err
	}
	s, err := c.Compile(url)
	if err != nil {
		return err
	}
	return s.Validate(v)
}

func usesRef(node any) bool {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if k == "$ref" || k == "$dynamicRef" || usesRef(v) {
				return true
			}
		}
	case []any:
		for _, v := range n {
			if usesRef(v) {
				return true
			}
		}
	}
	return false
}

// ApplyDefaults fills the schema's `default` values into doc (user fields only) wherever
// the field is absent. An explicit null is a value and is kept. A default inside an object
// is applied only when that object is present, or was itself just filled. It returns doc.
func (c *Collection) ApplyDefaults(doc map[string]any) map[string]any {
	for _, d := range c.Defaults {
		parent := doc
		for _, name := range d.Path[:len(d.Path)-1] {
			next, ok := parent[name].(map[string]any)
			if !ok {
				parent = nil
				break
			}
			parent = next
		}
		if parent == nil {
			continue
		}
		last := d.Path[len(d.Path)-1]
		if _, present := parent[last]; !present {
			parent[last] = docutil.DeepCopy(d.Value)
		}
	}
	return doc
}
