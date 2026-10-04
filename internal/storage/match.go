package storage

import (
	"cmp"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Match evaluates a filter against a document held in memory, with the
// semantics MongoDB gives the same filter, for the operators access rules
// produce (eq, ne, in, nin, the comparisons and isNull):
//
//   - a missing field is null: it equals nil, is not equal to anything else,
//     and an ordering comparison with it matches nothing;
//   - on an array, equality matches any element (and so do comparisons);
//   - numbers compare by value whether they are int64 or float64, and a
//     comparison only matches values of its own kind (numbers, strings,
//     dates), never across kinds.
//
// It lets rules be tested without a database. The other operators (text
// matching, between) are for the query language, and report an error.
func Match(doc Document, f Filter) (bool, error) {
	switch f := f.(type) {
	case Const:
		return bool(f), nil
	case And:
		for _, x := range f {
			if ok, err := Match(doc, x); err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case Or:
		for _, x := range f {
			ok, err := Match(doc, x)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case Not:
		ok, err := Match(doc, f.Filter)
		return !ok && err == nil, err
	case Condition:
		return matchCondition(doc, f)
	case nil:
		return true, nil
	}
	return false, fmt.Errorf("unknown filter %T", f)
}

// lookup reads a dot path from a document; a missing field is nil.
func lookup(doc map[string]any, path string) any {
	var v any = map[string]any(doc)
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func matchCondition(doc Document, c Condition) (bool, error) {
	v := lookup(doc, c.Field)
	switch c.Op {
	case OpEq:
		return equalsAny(v, c.Value), nil
	case OpNe:
		return !equalsAny(v, c.Value), nil
	case OpIsNull:
		want, ok := c.Value.(bool)
		if !ok {
			return false, fmt.Errorf("%s: $isNull needs a boolean", c.Field)
		}
		return (v == nil) == want, nil
	case OpIn, OpNin:
		list, ok := c.Value.([]any)
		if !ok {
			return false, fmt.Errorf("%s: %s needs a list", c.Field, c.Op)
		}
		found := false
		for _, x := range list {
			if equalsAny(v, x) {
				found = true
				break
			}
		}
		return found == (c.Op == OpIn), nil
	case OpLt, OpLte, OpGt, OpGte:
		// On an array, any element may satisfy the comparison.
		for _, x := range elements(v) {
			n, ok := compareValues(x, c.Value)
			if !ok {
				continue
			}
			if (c.Op == OpLt && n < 0) || (c.Op == OpLte && n <= 0) || (c.Op == OpGt && n > 0) || (c.Op == OpGte && n >= 0) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("%s: operator %s can't be evaluated in memory", c.Field, c.Op)
}

// elements is the values a comparison looks at: the elements of an array, or
// the value itself.
func elements(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return []any{v}
}

// equalsAny is MongoDB's equality: the value equals b, or, for an array, any
// element does (or the whole array does).
func equalsAny(v, b any) bool {
	if arr, ok := v.([]any); ok {
		for _, e := range arr {
			if equalValues(e, b) {
				return true
			}
		}
	}
	return equalValues(v, b)
}

func equalValues(a, b any) bool {
	if n, ok := compareValues(a, b); ok {
		return n == 0
	}
	return reflect.DeepEqual(a, b)
}

// compareValues orders two values of the same kind: numbers (int64 or
// float64), strings or times. Anything else isn't comparable.
func compareValues(a, b any) (int, bool) {
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			if xi, ok := a.(int64); ok {
				if yi, ok := b.(int64); ok {
					return cmp.Compare(xi, yi), true // exact for large integers
				}
			}
			return cmp.Compare(x, y), true
		}
		return 0, false
	}
	switch x := a.(type) {
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

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}
