package email

import (
	"fmt"
	"html"
	"strings"
)

// Owner templates (design/team-access.md §11) use a logic-less, Mustache-style syntax, so a
// company can edit its own emails without being able to run anything:
//
//	{{Link}}  {{User.Email}}  {{App.name}}  {{Data.days}}      a value, always escaped
//	{{#App.logo}} <img src="{{App.logo}}"> {{/App.logo}}        a section: when the value is there
//	{{^App.logo}} text only {{/App.logo}}                       an inverted section: when it is not
//	{{! a comment }}
//
// There are no includes, partials, helpers, raw output or changing delimiters: a tag that
// asks for one is an error with its line and column.

// SyntaxError is a problem in an owner template, with where it is.
type SyntaxError struct {
	Line, Column int
	Message      string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Message)
}

type node struct {
	kind     nodeKind
	text     string // literal text, or the dotted name of a variable or section
	children []node // of a section
}

type nodeKind int

const (
	nodeText nodeKind = iota
	nodeVar
	nodeSection
	nodeInverted
)

// Limits of an owner template, so one can't be made to cost much.
const (
	maxNesting     = 6
	maxSectionRuns = 1000
	// MaxTemplateBytes is the size of each of subject, text and HTML of an override.
	MaxTemplateBytes = 64 << 10
)

// Mustache is a parsed owner template.
type Mustache struct {
	nodes []node
}

// ParseMustache parses src, or says what is wrong and where.
func ParseMustache(src string) (*Mustache, *SyntaxError) {
	p := &mustacheParser{src: src, line: 1, col: 1}
	nodes, err := p.parse(0, "", 0, 0)
	if err != nil {
		return nil, err
	}
	return &Mustache{nodes: nodes}, nil
}

type mustacheParser struct {
	src       string
	pos       int
	line, col int
}

// advance moves over n bytes, keeping the line and column.
func (p *mustacheParser) advance(n int) {
	for _, r := range p.src[p.pos : p.pos+n] {
		if r == '\n' {
			p.line, p.col = p.line+1, 1
		} else {
			p.col++
		}
	}
	p.pos += n
}

func validName(name string) bool {
	if name == "." { // the item of a list being repeated
		return true
	}
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
		for i, r := range part {
			letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			digit := i > 0 && r >= '0' && r <= '9'
			if !letter && !digit {
				return false
			}
		}
	}
	return true
}

// parse reads until the end, or until the tag that closes section closing (depth > 0), which
// opened at openLine and openColumn.
func (p *mustacheParser) parse(depth int, closing string, openLine, openColumn int) ([]node, *SyntaxError) {
	var nodes []node
	for {
		i := strings.Index(p.src[p.pos:], "{{")
		if i < 0 {
			if closing != "" {
				return nil, &SyntaxError{openLine, openColumn, "the section {{#" + closing + "}} opened here is never closed"}
			}
			if rest := p.src[p.pos:]; rest != "" {
				nodes = append(nodes, node{kind: nodeText, text: rest})
				p.advance(len(rest))
			}
			return nodes, nil
		}
		if i > 0 {
			nodes = append(nodes, node{kind: nodeText, text: p.src[p.pos : p.pos+i]})
			p.advance(i)
		}
		line, col := p.line, p.col
		end := strings.Index(p.src[p.pos:], "}}")
		if end < 0 {
			return nil, &SyntaxError{line, col, "the tag is never closed (missing }})"}
		}
		tag := p.src[p.pos+2 : p.pos+end]
		if strings.HasPrefix(tag, "{") || strings.HasSuffix(tag, "}") {
			return nil, &SyntaxError{line, col, "{{{…}}} (raw output) isn't supported: values are always escaped"}
		}
		body := strings.TrimSpace(tag)
		p.advance(end + 2)
		switch {
		case strings.HasPrefix(body, "!"):
			// a comment
		case strings.HasPrefix(body, ">"):
			return nil, &SyntaxError{line, col, "partials and includes aren't supported"}
		case strings.HasPrefix(body, "&"):
			return nil, &SyntaxError{line, col, "{{& …}} (raw output) isn't supported: values are always escaped"}
		case strings.HasPrefix(body, "="):
			return nil, &SyntaxError{line, col, "changing delimiters isn't supported"}
		case strings.HasPrefix(body, "#"), strings.HasPrefix(body, "^"):
			name := strings.TrimSpace(body[1:])
			if !validName(name) {
				return nil, &SyntaxError{line, col, fmt.Sprintf("%q isn't a name (letters, digits and '_', with '.' between parts)", name)}
			}
			if depth+1 > maxNesting {
				return nil, &SyntaxError{line, col, fmt.Sprintf("sections nest at most %d deep", maxNesting)}
			}
			children, err := p.parse(depth+1, name, line, col)
			if err != nil {
				return nil, err
			}
			kind := nodeSection
			if body[0] == '^' {
				kind = nodeInverted
			}
			nodes = append(nodes, node{kind: kind, text: name, children: children})
		case strings.HasPrefix(body, "/"):
			name := strings.TrimSpace(body[1:])
			if closing == "" || name != closing {
				return nil, &SyntaxError{line, col, "{{/" + name + "}} closes a section that isn't open"}
			}
			return nodes, nil
		default:
			if !validName(body) {
				return nil, &SyntaxError{line, col, fmt.Sprintf("%q isn't a name (letters, digits and '_', with '.' between parts; helpers and expressions aren't supported)", body)}
			}
			nodes = append(nodes, node{kind: nodeVar, text: body})
		}
	}
}

// Variables lists the names the template prints or tests, each once, in order of appearance.
func (m *Mustache) Variables() []string {
	var out []string
	seen := map[string]bool{}
	var walk func([]node)
	walk = func(ns []node) {
		for _, n := range ns {
			if n.kind != nodeText && !seen[n.text] {
				seen[n.text] = true
				out = append(out, n.text)
			}
			walk(n.children)
		}
	}
	walk(m.nodes)
	return out
}

// Prints reports whether the template prints name (not only tests it).
func (m *Mustache) Prints(name string) bool {
	var walk func([]node) bool
	walk = func(ns []node) bool {
		for _, n := range ns {
			if (n.kind == nodeVar && n.text == name) || walk(n.children) {
				return true
			}
		}
		return false
	}
	return walk(m.nodes)
}

// Escapers: HTML for the HTML part, and for text and subject a plain one that only drops
// control characters (text isn't HTML, and the subject is one line).
var (
	// EscapeHTML escapes a value for the HTML part.
	EscapeHTML = html.EscapeString
	// EscapeText makes a value safe for the text part and the subject: no control characters
	// except a newline and a tab.
	EscapeText = func(s string) string {
		return strings.Map(func(r rune) rune {
			if r < ' ' && r != '\n' && r != '\t' || r == 0x7f {
				return -1
			}
			return r
		}, s)
	}
)

// Render renders the template with data (maps, slices and scalars), escaping every value with
// escape. A value that is missing prints nothing, as in Mustache.
func (m *Mustache) Render(data map[string]any, escape func(string) string) (string, error) {
	var b strings.Builder
	r := &mustacheRun{escape: escape, out: &b}
	if err := r.render(m.nodes, []any{data}); err != nil {
		return "", err
	}
	return b.String(), nil
}

type mustacheRun struct {
	escape func(string) string
	out    *strings.Builder
	runs   int
}

func lookup(stack []any, name string) (any, bool) {
	if name == "." {
		return stack[len(stack)-1], true
	}
	parts := strings.Split(name, ".")
	for i := len(stack) - 1; i >= 0; i-- {
		cur, ok := stack[i].(map[string]any)
		if !ok {
			continue
		}
		v, found := cur[parts[0]]
		for _, p := range parts[1:] {
			if !found {
				break
			}
			next, isMap := v.(map[string]any)
			if !isMap {
				found = false
				break
			}
			v, found = next[p]
		}
		if found {
			return v, true
		}
	}
	return nil, false
}

// truthy follows Mustache: nil, false, "" and empty lists and maps are not there.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func (r *mustacheRun) render(nodes []node, stack []any) error {
	for _, n := range nodes {
		switch n.kind {
		case nodeText:
			r.out.WriteString(n.text)
		case nodeVar:
			if v, ok := lookup(stack, n.text); ok && v != nil {
				switch v.(type) {
				case map[string]any, []any, []string:
					// a structure isn't printed
				default:
					r.out.WriteString(r.escape(fmt.Sprint(v)))
				}
			}
		case nodeInverted:
			if v, ok := lookup(stack, n.text); !ok || !truthy(v) {
				if err := r.render(n.children, stack); err != nil {
					return err
				}
			}
		case nodeSection:
			v, ok := lookup(stack, n.text)
			if !ok || !truthy(v) {
				continue
			}
			var items []any
			switch t := v.(type) {
			case []any:
				items = t
			case []string:
				for _, s := range t {
					items = append(items, s)
				}
			default:
				items = []any{v}
			}
			for _, item := range items {
				if r.runs++; r.runs > maxSectionRuns {
					return fmt.Errorf("the template repeats a section more than %d times", maxSectionRuns)
				}
				if err := r.render(n.children, append(stack, item)); err != nil {
					return err
				}
			}
		}
		if r.out.Len() > 4*MaxTemplateBytes {
			return fmt.Errorf("the message is larger than %d KiB", 4*MaxTemplateBytes>>10)
		}
	}
	return nil
}
