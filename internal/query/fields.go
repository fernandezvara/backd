package query

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// MaxSelectedFields is how many paths one `fields` parameter may name.
const MaxSelectedFields = 32

// Selection is a parsed `fields` parameter (design/field-projection.md): the paths of a document a
// response keeps. It is applied last, to the document as the caller sees it, so it never changes
// what a rule, a cursor, an expansion or a hidden field does.
type Selection struct {
	paths []string // sorted, deduplicated
	root  *selNode
}

type selNode struct {
	all      bool // the whole subtree is kept
	children map[string]*selNode
}

// ParseFields reads the `fields` parameter: comma-separated paths of declared fields (dotted for
// nested ones), `id`, `_meta` or one of the `_meta.…` system fields. An empty string means no
// selection (nil). `id` is always part of the answer, so naming it changes nothing.
func ParseFields(s string, fields map[string]registry.Field) (*Selection, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > MaxSelectedFields {
		return nil, Errors{{Path: "fields", Reason: fmt.Sprintf("must name at most %d paths", MaxSelectedFields)}}
	}
	var errs Errors
	seen := map[string]bool{}
	sel := &Selection{root: &selNode{children: map[string]*selNode{}}}
	for _, part := range parts {
		p := strings.TrimSpace(part)
		switch {
		case p == "":
			errs = append(errs, Error{Path: "fields", Reason: "empty field name"})
			continue
		case p == "id":
			continue // always returned
		case p == "_meta":
		case strings.HasPrefix(p, "_meta."):
			if _, ok := systemFields[p]; !ok {
				errs = append(errs, Error{Path: "fields." + p, Reason: "unknown field"})
				continue
			}
		default:
			if _, ok := fields[p]; !ok {
				errs = append(errs, Error{Path: "fields." + p, Reason: "unknown field"})
				continue
			}
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		sel.paths = append(sel.paths, p)
		sel.add(p)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	slices.Sort(sel.paths)
	return sel, nil
}

func (s *Selection) add(path string) {
	n := s.root
	for _, k := range strings.Split(path, ".") {
		if n.all {
			return // a parent is already selected
		}
		child := n.children[k]
		if child == nil {
			child = &selNode{children: map[string]*selNode{}}
			n.children[k] = child
		}
		n = child
	}
	n.all, n.children = true, nil
}

// Key identifies the selection (the sorted paths), for the validator of a cached answer.
func (s *Selection) Key() string {
	if s == nil {
		return ""
	}
	return "fields:" + strings.Join(s.paths, ",")
}

// Apply returns doc reduced to the selection: `id`, the selected paths, and `_expanded` when the
// request expanded references (it is computed from the stored document, not from the selected
// fields). A path through an array applies to every element. doc is not changed.
func (s *Selection) Apply(doc map[string]any) map[string]any {
	if s == nil {
		return doc
	}
	out := make(map[string]any, len(s.root.children)+2)
	if id, ok := doc["id"]; ok {
		out["id"] = id
	}
	if ex, ok := doc["_expanded"]; ok {
		out["_expanded"] = ex
	}
	for k, child := range s.root.children {
		if v, ok := doc[k]; ok {
			if kept, ok := project(v, child); ok {
				out[k] = kept
			}
		}
	}
	return out
}

func project(v any, n *selNode) (any, bool) {
	if n.all {
		return v, true
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(n.children))
		for k, child := range n.children {
			if sub, ok := x[k]; ok {
				if kept, ok := project(sub, child); ok {
					out[k] = kept
				}
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(x))
		for _, el := range x {
			if kept, ok := project(el, n); ok {
				out = append(out, kept)
			}
		}
		return out, true
	}
	return nil, false // a scalar where the path goes deeper: nothing to keep
}
