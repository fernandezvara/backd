package realtime

import (
	"strconv"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

// Key is an equality a subscription can only be visible under: every document it shows has
// one of Values at Field. It is what lets a change find the subscriptions it can matter to
// without asking all of them.
type Key struct {
	Field  string
	Values []any
}

// keyPriority orders where a subscription is filed when it has several keys: the grouping
// fields of organizations and teams first, as they split subscribers best.
var keyPriority = map[string]int{"_meta.org": 0, "_meta.team": 1}

// KeysOf extracts the keys of a filter: its top-level conditions (under And) that are an
// equality or a membership on a field. A condition under Or or Not is not a key, because the
// subscription may be visible without it. The result is only a shortcut: the whole filter
// still decides every candidate.
func KeysOf(f storage.Filter) []Key {
	var out []Key
	var walk func(f storage.Filter)
	walk = func(f storage.Filter) {
		switch f := f.(type) {
		case storage.And:
			for _, x := range f {
				walk(x)
			}
		case storage.Condition:
			switch f.Op {
			case storage.OpEq:
				if indexable(f.Value) {
					out = append(out, Key{Field: f.Field, Values: []any{f.Value}})
				}
			case storage.OpIn:
				list, _ := f.Value.([]any)
				if len(list) == 0 {
					return
				}
				for _, v := range list {
					if !indexable(v) {
						return
					}
				}
				out = append(out, Key{Field: f.Field, Values: list})
			}
		}
	}
	walk(f)
	return out
}

// best picks the key a subscription is filed under, or false when it has none.
func best(keys []Key) (Key, bool) {
	var b Key
	found, bp := false, 1<<30
	for _, k := range keys {
		p, ok := keyPriority[k.Field]
		if !ok {
			p = 100
		}
		if !found || p < bp {
			b, bp, found = k, p, true
		}
	}
	return b, found
}

func indexable(v any) bool { return keyOf(v) != "" }

// keyOf is the text a value is filed under: values that Match would call equal share it
// (numbers by value, whatever their type). "" is not indexable.
func keyOf(v any) string {
	switch t := v.(type) {
	case string:
		return "s:" + t
	case bool:
		return "b:" + strconv.FormatBool(t)
	case int:
		return "n:" + strconv.FormatFloat(float64(t), 'g', -1, 64)
	case int32:
		return "n:" + strconv.FormatFloat(float64(t), 'g', -1, 64)
	case int64:
		return "n:" + strconv.FormatFloat(float64(t), 'g', -1, 64)
	case float64:
		return "n:" + strconv.FormatFloat(t, 'g', -1, 64)
	case time.Time:
		return "t:" + strconv.FormatInt(t.UnixNano(), 10)
	}
	return ""
}

// collIndex files the subscriptions of one collection.
type collIndex struct {
	docs    map[string][]*Subscription            // by document id
	groups  map[string]map[string][]*Subscription // field → value → subscriptions
	fields  map[string]int                        // how many subscriptions are filed under a field
	rest    map[*Subscription]struct{}            // no usable key: checked on every change
	streams map[*Stream]int                       // streams with a subscription here
}

func newCollIndex() *collIndex {
	return &collIndex{
		docs: map[string][]*Subscription{}, groups: map[string]map[string][]*Subscription{},
		fields: map[string]int{}, rest: map[*Subscription]struct{}{}, streams: map[*Stream]int{},
	}
}

func (ci *collIndex) add(s *Subscription) {
	ci.streams[s.owner]++
	switch {
	case s.Doc != "":
		ci.docs[s.Doc] = append(ci.docs[s.Doc], s)
		s.filed = "doc"
	default:
		k, ok := best(s.Keys)
		if !ok {
			ci.rest[s] = struct{}{}
			s.filed = "scan"
			return
		}
		g := ci.groups[k.Field]
		if g == nil {
			g = map[string][]*Subscription{}
			ci.groups[k.Field] = g
		}
		seen := map[string]bool{}
		for _, v := range k.Values {
			key := keyOf(v)
			if seen[key] {
				continue
			}
			seen[key] = true
			g[key] = append(g[key], s)
		}
		s.keyField = k.Field
		s.keyValues = keysOf(k.Values)
		ci.fields[k.Field]++
		s.filed = "group"
	}
}

func keysOf(vs []any) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = keyOf(v)
	}
	return out
}

func (ci *collIndex) remove(s *Subscription) {
	if ci.streams[s.owner]--; ci.streams[s.owner] <= 0 {
		delete(ci.streams, s.owner)
	}
	switch s.filed {
	case "doc":
		ci.docs[s.Doc] = without(ci.docs[s.Doc], s)
		if len(ci.docs[s.Doc]) == 0 {
			delete(ci.docs, s.Doc)
		}
	case "scan":
		delete(ci.rest, s)
	case "group":
		g := ci.groups[s.keyField]
		for _, key := range s.keyValues {
			g[key] = without(g[key], s)
			if len(g[key]) == 0 {
				delete(g, key)
			}
		}
		if ci.fields[s.keyField]--; ci.fields[s.keyField] <= 0 {
			delete(ci.fields, s.keyField)
			delete(ci.groups, s.keyField)
		}
	}
	s.filed = ""
}

func without(list []*Subscription, s *Subscription) []*Subscription {
	for i, x := range list {
		if x == s {
			out := append([]*Subscription(nil), list[:i]...)
			return append(out, list[i+1:]...)
		}
	}
	return list
}

// candidates are the subscriptions a change may matter to: those filed under the document's
// id, those whose key matches the document before or after the change, and those with no key.
// Counts say how many came from groups and how many had to be scanned.
func (ci *collIndex) candidates(key string, before, after map[string]any) (out []*Subscription, indexed, scanned int) {
	seen := map[*Subscription]struct{}{}
	add := func(list []*Subscription) {
		for _, s := range list {
			if _, dup := seen[s]; !dup {
				seen[s] = struct{}{}
				out = append(out, s)
			}
		}
	}
	add(ci.docs[key])
	for field, g := range ci.groups {
		for _, doc := range []map[string]any{before, after} {
			if doc == nil {
				continue
			}
			for _, v := range elementsOf(lookupPath(doc, field)) {
				if k := keyOf(v); k != "" {
					add(g[k])
				}
			}
		}
	}
	indexed = len(out)
	for s := range ci.rest {
		if _, dup := seen[s]; !dup {
			seen[s] = struct{}{}
			out = append(out, s)
			scanned++
		}
	}
	return out, indexed, scanned
}

func elementsOf(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return []any{v}
}

func lookupPath(doc map[string]any, path string) any {
	var v any = doc
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[path[start:i]]
			start = i + 1
		}
	}
	return v
}
