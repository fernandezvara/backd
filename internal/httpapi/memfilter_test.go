package httpapi

import (
	"cmp"
	"reflect"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

// matches evaluates a storage filter against an in-memory document, with
// the MongoDB semantics the tests rely on: missing equals null, and
// equality on an array matches any element.
func matches(doc map[string]any, f storage.Filter) bool {
	switch f := f.(type) {
	case storage.Const:
		return bool(f)
	case storage.And:
		for _, x := range f {
			if !matches(doc, x) {
				return false
			}
		}
		return true
	case storage.Or:
		for _, x := range f {
			if matches(doc, x) {
				return true
			}
		}
		return false
	case storage.Not:
		return !matches(doc, f.Filter)
	case storage.Condition:
		return matchCond(doc, f)
	}
	panic("unknown filter")
}

func lookupPath(doc map[string]any, path string) any {
	var v any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func matchCond(doc map[string]any, c storage.Condition) bool {
	v := lookupPath(doc, c.Field)
	eq := func(a, b any) bool {
		if arr, ok := a.([]any); ok {
			for _, e := range arr {
				if reflect.DeepEqual(e, b) {
					return true
				}
			}
		}
		return reflect.DeepEqual(a, b)
	}
	switch c.Op {
	case storage.OpEq:
		return eq(v, c.Value)
	case storage.OpNe:
		return !eq(v, c.Value)
	case storage.OpIsNull:
		return (v == nil) == c.Value.(bool)
	case storage.OpIn:
		for _, x := range c.Value.([]any) {
			if eq(v, x) {
				return true
			}
		}
		return false
	case storage.OpLt, storage.OpLte, storage.OpGt, storage.OpGte:
		n, ok := compare(v, c.Value)
		if !ok {
			return false
		}
		switch c.Op {
		case storage.OpLt:
			return n < 0
		case storage.OpLte:
			return n <= 0
		case storage.OpGt:
			return n > 0
		}
		return n >= 0
	}
	panic("operator not supported by the test store: " + string(c.Op))
}

func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case int64:
		if y, ok := b.(int64); ok {
			return cmp.Compare(x, y), true
		}
	case float64:
		if y, ok := b.(float64); ok {
			return cmp.Compare(x, y), true
		}
	case string:
		if y, ok := b.(string); ok {
			return cmp.Compare(x, y), true
		}
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Compare(y), true
		}
	}
	return 0, false
}
