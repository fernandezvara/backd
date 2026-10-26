package query

import (
	"regexp"
	"strings"
)

// ExpandPath is one entry of an `expand` parameter: a reference name, or a chain of them
// (`author.company`) for a second level, with the fields to return of the last document.
type ExpandPath struct {
	// Names are the references followed, one per level.
	Names []string
	// Fields selects what to return of the last document; nil returns it whole.
	Fields []string
	// Text is the entry as written, for messages.
	Text string
}

var (
	expandName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	expandField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
)

// ParseExpand reads the `expand` parameter: entries separated by commas, each a reference
// name or a dotted chain of them, optionally followed by the fields to return in
// parentheses: `author(name,avatar),tags(name),author.company`. An empty value is no
// entries. Whether the names and fields exist is checked against the collections by the
// caller; this only reads the syntax.
func ParseExpand(s string) ([]ExpandPath, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []ExpandPath
	var errs Errors
	fail := func(part, reason string) {
		errs = append(errs, Error{Path: "expand", Reason: part + ": " + reason})
	}
	entries, ok := splitTop(s)
	if !ok {
		return nil, Errors{{Path: "expand", Reason: "unbalanced parentheses in " + s}}
	}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			fail("(empty)", "an empty entry: remove the extra comma")
			continue
		}
		head, fields, hasFields := strings.Cut(e, "(")
		p := ExpandPath{Text: e}
		if hasFields {
			if !strings.HasSuffix(fields, ")") || strings.ContainsAny(strings.TrimSuffix(fields, ")"), "()") {
				fail(e, "fields go in one pair of parentheses at the end: name(field,field)")
				continue
			}
			list := strings.TrimSuffix(fields, ")")
			bad := false
			for f := range strings.SplitSeq(list, ",") {
				f = strings.TrimSpace(f)
				if !expandField.MatchString(f) {
					fail(e, "\""+f+"\" is not a field name")
					bad = true
					break
				}
				p.Fields = append(p.Fields, f)
			}
			if bad {
				continue
			}
		}
		for n := range strings.SplitSeq(strings.TrimSpace(head), ".") {
			if !expandName.MatchString(n) {
				fail(e, "\""+n+"\" is not a reference name")
				p.Names = nil
				break
			}
			p.Names = append(p.Names, n)
		}
		if len(p.Names) > 0 {
			out = append(out, p)
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// splitTop splits s at the commas outside parentheses; ok is false when they don't balance.
func splitTop(s string) (parts []string, ok bool) {
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, false
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, false
	}
	return append(parts, s[start:]), true
}
