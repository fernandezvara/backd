package exportpkg

import (
	"strings"
)

// Select keeps of a document its id and the given field paths ("a", "a.b", "_meta.owner"):
// a path naming an object keeps all of it, one that is missing in the document adds nothing.
// "_meta" is only present when a path asks for it.
func Select(doc map[string]any, paths []string) map[string]any {
	out := map[string]any{}
	if id, ok := doc["id"]; ok {
		out["id"] = id
	}
	for _, p := range paths {
		copyPath(out, doc, strings.Split(p, "."))
	}
	return out
}

func copyPath(dst, src map[string]any, parts []string) {
	v, ok := src[parts[0]]
	if !ok {
		return
	}
	if len(parts) == 1 {
		dst[parts[0]] = v
		return
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return
	}
	next, _ := dst[parts[0]].(map[string]any)
	if next == nil {
		next = map[string]any{}
	}
	copyPath(next, sub, parts[1:])
	if len(next) > 0 {
		dst[parts[0]] = next
	}
}

// Remove deletes the value at a dotted path of a document, if it is there.
func Remove(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// Set puts a value at a dotted path of a document, making the objects on the way.
func Set(doc map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

// Get returns the value at a dotted path of a document, or nil.
func Get(doc map[string]any, path string) any {
	var cur any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}
