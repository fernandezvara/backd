package rules

import "github.com/expr-lang/expr/ast"

// tri is a truth value that may be unknown, for reading a rule without a
// document: `document.published == true` is unknown, `user != nil` is false.
type tri int

const (
	unknown tri = iota
	yes
	no
)

func (t tri) not() tri {
	switch t {
	case yes:
		return no
	case no:
		return yes
	}
	return unknown
}

// CanAllowAnonymous reports whether the rule may be true for a caller without
// credentials: false only when it is false whatever the document holds, such
// as `user != nil`, `hasRole(user, 'admin')` or `hasConsent(user, 'terms')`. It reads the rule's syntax
// tree with three values; `document` and `now` are unknown.
func (r *Rule) CanAllowAnonymous() bool { return anonymous(r.tree) != no }

// UsesNow reports whether the rule depends on the time of the request.
func (r *Rule) UsesNow() bool { return refs(r.tree, "now") }

func anonymous(n ast.Node) tri {
	switch n := n.(type) {
	case *ast.BoolNode:
		if n.Value {
			return yes
		}
		return no
	case *ast.UnaryNode:
		if n.Operator == "!" || n.Operator == "not" {
			return anonymous(n.Node).not()
		}
	case *ast.BinaryNode:
		switch n.Operator {
		case "&&", "and":
			l, r := anonymous(n.Left), anonymous(n.Right)
			switch {
			case l == no || r == no:
				return no
			case l == yes && r == yes:
				return yes
			}
			return unknown
		case "||", "or":
			l, r := anonymous(n.Left), anonymous(n.Right)
			switch {
			case l == yes || r == yes:
				return yes
			case l == no && r == no:
				return no
			}
			return unknown
		case "==", "!=":
			if (isIdent(n.Left, "user") && isNil(n.Right)) || (isIdent(n.Right, "user") && isNil(n.Left)) {
				if n.Operator == "==" {
					return yes
				}
				return no
			}
		}
	case *ast.CallNode:
		// hasRole(user, …) is false for anonymous callers.
		if id, ok := n.Callee.(*ast.IdentifierNode); ok && (id.Value == "hasRole" || id.Value == "hasConsent") {
			return no
		}
	}
	return unknown
}
