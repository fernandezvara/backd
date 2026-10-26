// Package docutil has the small operations on JSON-shaped documents (nested map[string]any and
// []any) that several packages need: copying, merge patches and dot paths. It imports only the
// standard library, so any package may use it.
package docutil

import "strings"

// DeepCopy copies the maps and slices of a JSON-shaped value; other values are shared.
func DeepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = DeepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = DeepCopy(e)
		}
		return out
	}
	return v
}

// MergePatch applies an RFC 7396 JSON Merge Patch object to target, which is modified in place
// and returned. A null value removes the member.
func MergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			tv, _ := target[k].(map[string]any)
			target[k] = MergePatch(tv, pv)
		default:
			target[k] = v
		}
	}
	return target
}

// GetPath reads a dot path ("a.b.c") of a document; ok is false when a member on the way is
// missing or is not an object.
func GetPath(doc map[string]any, path string) (v any, ok bool) {
	var cur any = doc
	for p := range strings.SplitSeq(path, ".") {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil, false
		}
		if cur, isMap = m[p]; !isMap {
			return nil, false
		}
	}
	return cur, true
}

// Get is GetPath for callers that treat a missing path as nil.
func Get(doc map[string]any, path string) any {
	v, _ := GetPath(doc, path)
	return v
}

// SetPath puts v at a dot path of a document, making the objects on the way.
func SetPath(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := doc[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[p] = next
		}
		doc = next
	}
	doc[parts[len(parts)-1]] = v
}

// SetPathExisting puts v at a dot path of a document in place; when an object on the way is
// missing it does nothing.
func SetPathExisting(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := doc[p].(map[string]any)
		if !ok {
			return
		}
		doc = next
	}
	doc[parts[len(parts)-1]] = v
}

// RemovePath deletes the value at a dot path of a document, if it is there.
func RemovePath(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := doc[p].(map[string]any)
		if !ok {
			return
		}
		doc = next
	}
	delete(doc, parts[len(parts)-1])
}

// CopyPath copies the value at path (the parts of a dot path) from src into dst, keeping the
// nesting. A missing path adds nothing, and no empty object is left behind.
func CopyPath(dst, src map[string]any, path []string) {
	v, ok := src[path[0]]
	if !ok {
		return
	}
	if len(path) == 1 {
		dst[path[0]] = v
		return
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return
	}
	next, _ := dst[path[0]].(map[string]any)
	if next == nil {
		next = map[string]any{}
	}
	CopyPath(next, sub, path[1:])
	if len(next) > 0 {
		dst[path[0]] = next
	}
}

// DeepCopyMap is DeepCopy for a document.
func DeepCopyMap(m map[string]any) map[string]any {
	return DeepCopy(m).(map[string]any)
}
