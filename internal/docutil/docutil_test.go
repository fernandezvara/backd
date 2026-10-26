package docutil

import (
	"reflect"
	"testing"
)

func TestDeepCopyIsIndependent(t *testing.T) {
	src := map[string]any{"a": map[string]any{"b": []any{1, map[string]any{"c": 2}}}}
	cp := DeepCopy(src).(map[string]any)
	cp["a"].(map[string]any)["b"].([]any)[1].(map[string]any)["c"] = 3
	if src["a"].(map[string]any)["b"].([]any)[1].(map[string]any)["c"] != 2 {
		t.Fatal("copy shares nested values with the source")
	}
}

func TestMergePatch(t *testing.T) {
	target := map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": 3}}
	got := MergePatch(target, map[string]any{"a": nil, "b": map[string]any{"c": 9, "e": 4}, "f": "x"})
	want := map[string]any{"b": map[string]any{"c": 9, "d": 3, "e": 4}, "f": "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := MergePatch(nil, map[string]any{"a": 1}); got["a"] != 1 {
		t.Fatalf("nil target: %v", got)
	}
}

func TestPaths(t *testing.T) {
	doc := map[string]any{"a": map[string]any{"b": 1, "n": nil}, "s": "x"}
	if v, ok := GetPath(doc, "a.b"); !ok || v != 1 {
		t.Fatalf("a.b = %v %v", v, ok)
	}
	if v, ok := GetPath(doc, "a.n"); !ok || v != nil {
		t.Fatalf("a.n = %v %v", v, ok)
	}
	for _, p := range []string{"a.x", "s.y", "z.y"} {
		if _, ok := GetPath(doc, p); ok {
			t.Fatalf("%s found", p)
		}
		if Get(doc, p) != nil {
			t.Fatalf("Get %s not nil", p)
		}
	}

	SetPathExisting(doc, "z.y", 1)
	if _, ok := doc["z"]; ok {
		t.Fatal("SetPathExisting made an object")
	}
	SetPathExisting(doc, "a.b", 2)
	SetPath(doc, "z.y.w", 3)
	if Get(doc, "a.b") != 2 || Get(doc, "z.y.w") != 3 {
		t.Fatalf("doc = %v", doc)
	}
	RemovePath(doc, "z.y.w")
	RemovePath(doc, "q.r")
	if _, ok := GetPath(doc, "z.y.w"); ok {
		t.Fatal("still there")
	}
}

func TestCopyPath(t *testing.T) {
	src := map[string]any{"a": map[string]any{"b": 1, "c": 2}, "s": "x"}
	dst := map[string]any{}
	CopyPath(dst, src, []string{"a", "b"})
	CopyPath(dst, src, []string{"a", "missing"})
	CopyPath(dst, src, []string{"s", "deeper"})
	CopyPath(dst, src, []string{"none", "x"})
	want := map[string]any{"a": map[string]any{"b": 1}}
	if !reflect.DeepEqual(dst, want) {
		t.Fatalf("got %v, want %v", dst, want)
	}
}
