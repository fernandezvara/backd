package rules

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/expr-lang/expr/ast"
)

var comparisons = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

// check runs the static checks that expr's type checker can't do.
func check(r *Rule, schema Schema) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	// Field paths: only complete member chains are checked.
	checkPaths(r.tree, schema, add)

	// Variables available per operation.
	walk(r.tree, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.IdentifierNode:
			if r.Op == Invoke && (n.Value == "document" || n.Value == "data" || n.Value == "changed") {
				add("`%s` is not available in invoke rules: they see only `user`", n.Value)
				return
			}
			switch n.Value {
			case "document":
				if r.Op == Create {
					add("`document` is not available on create (use `data`)")
				}
			case "data":
				if r.Op == Read || r.Op == Delete || r.Op == Restore || r.Op == Purge {
					add("`data` is only available on create and update")
				}
			case "changed":
				if r.Op != Update {
					add("changed() is only available on update")
				}
			}
		}
	})

	// Declared role names.
	walk(r.tree, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.CallNode:
			if id, ok := n.Callee.(*ast.IdentifierNode); ok && id.Value == "hasRole" {
				for _, a := range n.Arguments[1:] {
					s, ok := a.(*ast.StringNode)
					switch {
					case !ok:
						add("hasRole: role names must be string literals")
					case !slices.Contains(schema.Roles, s.Value):
						add("role %q is not declared in realm.yaml", s.Value)
					}
				}
			}
		case *ast.BinaryNode:
			if s, ok := n.Left.(*ast.StringNode); ok && n.Operator == "in" && isUserField(n.Right, "roles") && !slices.Contains(schema.Roles, s.Value) {
				add("role %q is not declared in realm.yaml", s.Value)
			}
		}
	})

	// Every user field access must be guarded by a nil check.
	checkGuards(r.tree, false, func(n ast.Node) {
		add("%s needs a guard for anonymous callers, e.g. `user != nil && %s`", n.String(), n.String())
	})

	// now only compares with timestamps: _meta's, and fields stored as dates.
	allowed := map[ast.Node]bool{}
	walk(r.tree, func(n ast.Node) {
		b, ok := n.(*ast.BinaryNode)
		if !ok || !comparisons[b.Operator] {
			return
		}
		for _, sides := range [][2]ast.Node{{b.Left, b.Right}, {b.Right, b.Left}} {
			if isTimestamp(sides[0], schema) && !refs(sides[1], "document") && !refs(sides[1], "data") {
				walk(sides[1], func(m ast.Node) { allowed[m] = true })
			}
		}
	})
	walk(r.tree, func(n ast.Node) {
		if id, ok := n.(*ast.IdentifierNode); ok && id.Value == "now" && !allowed[n] {
			add("`now` can only be compared with a timestamp: document._meta.created_at, document._meta.updated_at, or a field stored as a date (x-backd-store: date)")
		}
	})

	// A field stored as a date is a time in rules: it compares with `now`, nil
	// or another date, never with text, which the database would not match the
	// way the rule says.
	walk(r.tree, func(n ast.Node) {
		b, ok := n.(*ast.BinaryNode)
		if !ok || !(comparisons[b.Operator] || b.Operator == "in") {
			return
		}
		for i, sides := range [][2]ast.Node{{b.Left, b.Right}, {b.Right, b.Left}} {
			p, ok := dateFieldPath(sides[0], schema)
			if !ok || (b.Operator == "in" && i == 1) {
				continue
			}
			other := sides[1]
			if b.Operator != "in" && (isNil(other) || refs(other, "now") && !refs(other, "user") || isTimestamp(other, schema)) {
				continue
			}
			add("%s is stored as a date: compare it with `now` (for example `now - duration('24h')`), with nil or with another date, not with text or user fields", p)
		}
	})

	// Read rules become database filters, and so do restore rules (they decide
	// which deleted documents a list shows).
	if r.Op == Read || r.Op == Restore {
		if err := filterable(r.tree, schema); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(dedupe(errs)...)
}

// checkPaths checks that document and data paths exist. It looks at
// whole member chains, not at their prefixes.
func checkPaths(n ast.Node, schema Schema, add func(string, ...any)) {
	if c, ok := n.(*ast.ChainNode); ok {
		n = c.Node
	}
	if m, ok := n.(*ast.MemberNode); ok {
		root, path, ok := memberPath(m)
		if ok && (root == "document" || root == "data") {
			known := schema.DocumentField
			if root == "data" {
				known = schema.DataField
			}
			switch {
			case path == "":
				add("use dot paths with literal names on %s, e.g. %s.title", root, root)
			case !known(path):
				add("unknown field %s.%s (not in schema.json)", root, path)
			}
			// Computed properties may hold further paths.
			for x := ast.Node(m); ; {
				mm, ok := x.(*ast.MemberNode)
				if !ok {
					break
				}
				if _, lit := mm.Property.(*ast.StringNode); !lit {
					checkPaths(mm.Property, schema, add)
				}
				x = mm.Node
			}
			return
		}
	}
	for _, c := range children(n) {
		checkPaths(c, schema, add)
	}
}

// checkGuards reports user field accesses not guarded by a nil check. A
// node is guarded on the right of `user != nil && …` or `user == nil || …`
// (directly or through enclosing && / ||).
func checkGuards(n ast.Node, guarded bool, report func(ast.Node)) {
	switch n := n.(type) {
	case *ast.BinaryNode:
		switch n.Operator {
		case "&&", "and":
			checkGuards(n.Left, guarded, report)
			checkGuards(n.Right, guarded || hasTerm(n.Left, "&&", "!="), report)
			return
		case "||", "or":
			checkGuards(n.Left, guarded, report)
			checkGuards(n.Right, guarded || hasTerm(n.Left, "||", "=="), report)
			return
		}
	case *ast.MemberNode:
		if !guarded && isUserMember(n) {
			report(n)
			return
		}
	case *ast.CallNode:
		// hasRole(user, …) is false for anonymous callers: no guard needed.
		if id, ok := n.Callee.(*ast.IdentifierNode); ok && id.Value == "hasRole" {
			for _, a := range n.Arguments[1:] {
				checkGuards(a, guarded, report)
			}
			return
		}
	}
	for _, c := range children(n) {
		checkGuards(c, guarded, report)
	}
}

// hasTerm reports whether n, split on op (&& or ||), has a term
// `user <cmp> nil`.
func hasTerm(n ast.Node, op, cmp string) bool {
	if b, ok := n.(*ast.BinaryNode); ok {
		if b.Operator == op || (op == "&&" && b.Operator == "and") || (op == "||" && b.Operator == "or") {
			return hasTerm(b.Left, op, cmp) || hasTerm(b.Right, op, cmp)
		}
		if b.Operator == cmp {
			return (isIdent(b.Left, "user") && isNil(b.Right)) || (isNil(b.Left) && isIdent(b.Right, "user"))
		}
	}
	return false
}

// filterable checks that a read rule can be turned into a database
// filter: boolean combinations of comparisons between a document field and
// a value that doesn't depend on the document.
func filterable(n ast.Node, schema Schema) error {
	if !refs(n, "document") {
		return nil // evaluated per request
	}
	switch n := n.(type) {
	case *ast.BinaryNode:
		switch n.Operator {
		case "&&", "and", "||", "or":
			return errors.Join(filterable(n.Left, schema), filterable(n.Right, schema))
		case "in":
			if p, ok := docPath(n.Left); ok && !refs(n.Right, "document") {
				return scalar(schema, p) // field in [values]
			}
			if p, ok := docPath(n.Right); ok && !refs(n.Left, "document") {
				if !schema.ArrayField(p) {
					return fmt.Errorf("read rules: `value in document.%s` needs document.%s to be declared as an array in schema.json", p, p)
				}
				return nil // value in array field
			}
		default:
			if comparisons[n.Operator] {
				if p, ok := docPath(n.Left); ok && !refs(n.Right, "document") {
					return scalar(schema, p)
				}
				if p, ok := docPath(n.Right); ok && !refs(n.Left, "document") {
					return scalar(schema, p)
				}
			}
		}
	case *ast.UnaryNode:
		if n.Operator == "!" || n.Operator == "not" {
			return filterable(n.Node, schema)
		}
	case *ast.MemberNode:
		if p, ok := docPath(n); ok {
			return scalar(schema, p) // a boolean field
		}
	}
	return fmt.Errorf("read rules must be database filters: `%s` isn't a comparison between a document field and a value", n.String())
}

// scalar checks that a read rule compares a single-valued field.
func scalar(schema Schema, path string) error {
	switch {
	case schema.ScalarField(path):
		return nil
	case schema.ArrayField(path):
		return fmt.Errorf("read rules: document.%s is an array, which the database compares element by element; use `value in document.%s`", path, path)
	}
	return fmt.Errorf("read rules: document.%s must be declared in schema.json with a type that isn't an array (and not inside an array) to be compared", path)
}

// docPath returns the dot path of a `document.a.b` member chain.
func docPath(n ast.Node) (string, bool) {
	root, path, ok := memberPath(n)
	return path, ok && root == "document" && path != ""
}

// memberPath splits a member chain like document.a.b into its root
// identifier and dot path. path is "" when a property isn't a literal name.
func memberPath(n ast.Node) (root, path string, ok bool) {
	if c, isChain := n.(*ast.ChainNode); isChain {
		n = c.Node
	}
	var parts []string
	literal := true
	for {
		switch m := n.(type) {
		case *ast.MemberNode:
			s, isStr := m.Property.(*ast.StringNode)
			if !isStr || m.Method {
				literal = false
			} else {
				parts = append(parts, s.Value)
			}
			n = m.Node
			continue
		case *ast.IdentifierNode:
			if !literal {
				return m.Value, "", true
			}
			if len(parts) == 0 {
				return "", "", false
			}
			slices.Reverse(parts)
			return m.Value, strings.Join(parts, "."), true
		}
		return "", "", false
	}
}

func isUserMember(n *ast.MemberNode) bool {
	root, _, ok := memberPath(n)
	return ok && root == "user"
}

func isUserField(n ast.Node, field string) bool {
	root, path, ok := memberPath(n)
	return ok && root == "user" && path == field
}

func isMetaTimestamp(n ast.Node) bool {
	p, ok := docPath(n)
	return ok && (p == "_meta.created_at" || p == "_meta.updated_at")
}

// dateFieldPath returns the display path ("document.starts_at") of a member
// chain on a field stored as a date.
func dateFieldPath(n ast.Node, schema Schema) (string, bool) {
	if schema.DateField == nil {
		return "", false
	}
	root, path, ok := memberPath(n)
	if !ok || path == "" || (root != "document" && root != "data") || !schema.DateField(path) {
		return "", false
	}
	return root + "." + path, true
}

// isTimestamp reports whether n is a time: a _meta timestamp or a field stored as a date.
func isTimestamp(n ast.Node, schema Schema) bool {
	if isMetaTimestamp(n) {
		return true
	}
	_, ok := dateFieldPath(n, schema)
	return ok
}

func isIdent(n ast.Node, name string) bool {
	id, ok := n.(*ast.IdentifierNode)
	return ok && id.Value == name
}

func isNil(n ast.Node) bool {
	_, ok := n.(*ast.NilNode)
	return ok
}

// refs reports whether n uses the identifier name.
func refs(n ast.Node, name string) bool {
	found := false
	walk(n, func(m ast.Node) { found = found || isIdent(m, name) })
	return found
}

func walk(n ast.Node, f func(ast.Node)) {
	if n == nil {
		return
	}
	f(n)
	for _, c := range children(n) {
		walk(c, f)
	}
}

// children returns the direct sub-nodes of n.
func children(n ast.Node) []ast.Node {
	switch n := n.(type) {
	case *ast.UnaryNode:
		return []ast.Node{n.Node}
	case *ast.BinaryNode:
		return []ast.Node{n.Left, n.Right}
	case *ast.ChainNode:
		return []ast.Node{n.Node}
	case *ast.MemberNode:
		return []ast.Node{n.Node, n.Property}
	case *ast.SliceNode:
		return []ast.Node{n.Node, n.From, n.To}
	case *ast.CallNode:
		return append([]ast.Node{n.Callee}, n.Arguments...)
	case *ast.BuiltinNode:
		return n.Arguments
	case *ast.PredicateNode:
		return []ast.Node{n.Node}
	case *ast.ConditionalNode:
		return []ast.Node{n.Cond, n.Exp1, n.Exp2}
	case *ast.VariableDeclaratorNode:
		return []ast.Node{n.Value, n.Expr}
	case *ast.SequenceNode:
		return n.Nodes
	case *ast.ArrayNode:
		return n.Nodes
	case *ast.MapNode:
		return n.Pairs
	case *ast.PairNode:
		return []ast.Node{n.Key, n.Value}
	}
	return nil
}

func dedupe(errs []error) []error {
	seen := map[string]bool{}
	var out []error
	for _, e := range errs {
		if !seen[e.Error()] {
			seen[e.Error()] = true
			out = append(out, e)
		}
	}
	return out
}
