package typegen

import (
	"fmt"
	"slices"
	"strings"
)

// Diff lists the type-level differences between two generations, one line each, for pull
// request reviews: `+` something was added, `-` removed, `~` its type changed. Empty when
// the types are the same.
func Diff(old, now Info) []string {
	var out []string
	seen := map[string]bool{}
	var names []string
	for k := range old.Entries {
		seen[k] = true
		names = append(names, k)
	}
	for k := range now.Entries {
		if !seen[k] {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	for _, k := range names {
		o, hadOld := old.Entries[k]
		n, hasNew := now.Entries[k]
		what := describe(k)
		switch {
		case !hadOld:
			out = append(out, fmt.Sprintf("+ %s%s", what, typeSuffix(n)))
		case !hasNew:
			out = append(out, fmt.Sprintf("- %s", what))
		case o != n:
			out = append(out, fmt.Sprintf("~ %s: %s → %s", what, orNone(o), orNone(n)))
		}
	}
	return out
}

func typeSuffix(t string) string {
	if t == "" {
		return ""
	}
	return ": " + t
}

func orNone(t string) string {
	if t == "" {
		return "(none)"
	}
	return t
}

// describe turns an entry key ("field main/orders note") into words.
func describe(key string) string {
	kind, rest, _ := strings.Cut(key, " ")
	switch kind {
	case "function":
		name, part, ok := strings.Cut(rest, " ")
		if ok {
			return fmt.Sprintf("function %s %s", name, part)
		}
		return "function " + rest
	case "field":
		coll, path, _ := strings.Cut(rest, " ")
		return fmt.Sprintf("field %s of %s", path, coll)
	}
	return kind + " " + rest
}
