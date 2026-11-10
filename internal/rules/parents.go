package rules

import (
	"errors"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"

	"github.com/fernandezvara/backd/internal/storage"
)

// Rules that follow a reference (design/parent-rules.md).
//
//	read: user != nil && user.id in parent(document.list_id).members
//
// parent(document.list_id) is the document that the reference whose source field is
// list_id points to. Each comparison that reads from it is one "unit": it holds when the
// parent exists (and isn't deleted) and the comparison is true of it. A unit is a boolean,
// so it combines with && || ! like any other term; a missing parent makes it false.
//
// A unit is turned into a hidden call, __parent(idx, key), so the rest of the engine
// deals with one more kind of leaf, like the organization helpers:
//   - in memory (Allow) the call fetches the parent by key and matches it against the
//     unit's filter (the same code a read rule's pushdown uses);
//   - as a filter (Filter) the unit's filter is resolved to the keys of the matching parents,
//     and the term becomes `list_id in keys` (a semi-join, worked out per request, so the
//     result is a plain condition that storage.Match and realtime apply unchanged).

const (
	parentFunc = "__parent"
	envVar     = "_env"
	parentsVar = "parents"
)

// Parents is how rules reach the parent documents. ref is the name of a reference declared in
// the collection's collection.yaml. Implementations only see live (not deleted) documents.
type Parents interface {
	// Get returns the parent whose reference key is key, or nil if there is none.
	Get(ref string, key any) (map[string]any, error)
	// Keys returns the reference key of every parent matching f. It fails when there are more
	// than the implementation is willing to return, so a rule never silently shows less.
	Keys(ref string, f storage.Filter) ([]any, error)
}

// ParentRef describes the reference whose source field is a path of the collection.
type ParentRef struct {
	Name string
	Many bool
}

// ParentTarget is what the rules of a parent unit are checked against once the whole realm is loaded.
type ParentTarget struct {
	Schema Schema
}

// parentUnit is one comparison on a parent document.
type parentUnit struct {
	Ref   string   // name of the reference
	Field string   // dot path of the source field on this collection
	Pred  ast.Node // the comparison, with the parent as `document`
	bound bool
}

// ParentUse says a rule follows a reference.
type ParentUse struct{ Ref, Field string }

func isParentCall(n ast.Node) (*ast.CallNode, bool) {
	c, ok := n.(*ast.CallNode)
	if !ok {
		return nil, false
	}
	id, ok := c.Callee.(*ast.IdentifierNode)
	return c, ok && id.Value == "parent"
}

// parentUnitCall recognizes the hidden call a unit was replaced with.
func parentUnitCall(n ast.Node) (idx int, key ast.Node, ok bool) {
	c, isCall := n.(*ast.CallNode)
	if !isCall {
		return 0, nil, false
	}
	id, isID := c.Callee.(*ast.IdentifierNode)
	if !isID || id.Value != parentFunc || len(c.Arguments) < 2 {
		return 0, nil, false
	}
	i, isInt := c.Arguments[len(c.Arguments)-2].(*ast.IntegerNode)
	if !isInt {
		return 0, nil, false
	}
	return i.Value, c.Arguments[len(c.Arguments)-1], true
}

func hasParent(n ast.Node) bool {
	found := false
	walk(n, func(m ast.Node) {
		if _, ok := isParentCall(m); ok {
			found = true
		}
	})
	return found
}

// extractParents replaces every term that reads from a parent with a call to the hidden
// function, and returns the units. tree is changed in place.
func (r *Rule) extractParents(tree ast.Node, schema Schema) (ast.Node, error) {
	if !hasParent(tree) {
		return tree, nil
	}
	if r.Op == Trigger || r.Op == Invoke || r.Op == Config || r.Op == Reveal {
		return nil, errors.New("parent() is only available in read, create, update, delete, restore and purge rules")
	}
	var errs []error
	var rewrite func(n ast.Node) ast.Node
	rewrite = func(n ast.Node) ast.Node {
		if !hasParent(n) {
			return n
		}
		switch n := n.(type) {
		case *ast.BinaryNode:
			switch n.Operator {
			case "&&", "and", "||", "or":
				n.Left, n.Right = rewrite(n.Left), rewrite(n.Right)
				return n
			}
		case *ast.UnaryNode:
			if n.Operator == "!" || n.Operator == "not" {
				n.Node = rewrite(n.Node)
				return n
			}
		}
		u, key, err := r.makeUnit(n, schema)
		if err != nil {
			errs = append(errs, err)
			return n
		}
		r.units = append(r.units, u)
		return &ast.CallNode{
			Callee:    &ast.IdentifierNode{Value: parentFunc},
			Arguments: []ast.Node{&ast.IntegerNode{Value: len(r.units) - 1}, key},
		}
	}
	out := rewrite(tree)
	if len(errs) > 0 {
		return nil, errors.Join(dedupe(errs)...)
	}
	return out, nil
}

// makeUnit checks a term that reads from a parent and returns its unit and the key expression
// (document.list_id) it is looked up with.
func (r *Rule) makeUnit(term ast.Node, schema Schema) (*parentUnit, ast.Node, error) {
	var calls []*ast.CallNode
	// Everything outside the parent calls must be known before the parent is fetched.
	var outside func(n ast.Node) error
	outside = func(n ast.Node) error {
		if c, ok := isParentCall(n); ok {
			calls = append(calls, c)
			return nil
		}
		if id, ok := n.(*ast.IdentifierNode); ok {
			switch id.Value {
			case "document", "data", "before":
				return fmt.Errorf("`%s` is compared with a field of parent(...): the comparison is made on the parent, so the other side can't depend on the document or the request body (use `user`, constants or appConfig)", term.String())
			}
		}
		if _, _, ok := orgCall(n); ok {
			return fmt.Errorf("`%s`: organization helpers can't be used in a comparison on parent(...)", term.String())
		}
		if c, ok := n.(*ast.CallNode); ok {
			if id, ok := c.Callee.(*ast.IdentifierNode); ok && id.Value == "changed" {
				return errors.New("changed() can't be used in a comparison on parent(...)")
			}
		}
		for _, c := range children(n) {
			if err := outside(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := outside(term); err != nil {
		return nil, nil, err
	}
	var key ast.Node
	var field, ref string
	for _, c := range calls {
		if len(c.Arguments) != 1 {
			return nil, nil, errors.New("parent() takes one argument: the field that holds the reference, such as parent(document.list_id)")
		}
		root, path, ok := memberPath(c.Arguments[0])
		if !ok || path == "" || (root != "document" && root != "data") {
			return nil, nil, fmt.Errorf("parent(%s): give the field that holds the reference as document.field or data.field, with literal names", c.Arguments[0].String())
		}
		info, ok := ParentRef{}, false
		if schema.ParentRef != nil {
			info, ok = schema.ParentRef(path)
		}
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("parent(%s): %q isn't the source field of a reference; declare one under `references:` in collection.yaml", c.Arguments[0].String(), path)
		case info.Many:
			return nil, nil, fmt.Errorf("parent(%s): reference %q holds a list of ids (many: true); parent() follows a reference to one document", c.Arguments[0].String(), info.Name)
		}
		if key != nil && key.String() != c.Arguments[0].String() {
			return nil, nil, fmt.Errorf("a comparison can use one parent: %s and %s differ; compare them separately", key.String(), c.Arguments[0].String())
		}
		key, field, ref = c.Arguments[0], path, info.Name
	}
	// The predicate: the term with each parent(x) read as `document`.
	t, err := parser.Parse(term.String())
	if err != nil {
		return nil, nil, err
	}
	pred := t.Node
	st := &parentStripper{wrapped: map[ast.Node]bool{}}
	ast.Walk(&pred, st)
	for _, c := range st.calls {
		if !st.wrapped[c] {
			return nil, nil, fmt.Errorf("`%s`: parent(...) must be followed by a field name, such as parent(%s).members", term.String(), key.String())
		}
	}
	return &parentUnit{Ref: ref, Field: field, Pred: pred}, key, nil
}

// parentStripper reads parent(x).field as document.field. The walk reaches a call before the
// member that reads from it, so the calls are matched up afterwards.
type parentStripper struct {
	calls   []ast.Node
	wrapped map[ast.Node]bool
}

func (p *parentStripper) Visit(node *ast.Node) {
	if m, ok := (*node).(*ast.MemberNode); ok {
		if _, isParent := isParentCall(m.Node); isParent {
			p.wrapped[m.Node] = true
			m.Node = &ast.IdentifierNode{Value: "document"}
		}
		return
	}
	if _, ok := isParentCall(*node); ok {
		p.calls = append(p.calls, *node)
	}
}

// ParentUses lists the references the rule follows.
func (r *Rule) ParentUses() []ParentUse {
	var out []ParentUse
	for _, u := range r.units {
		out = append(out, ParentUse{Ref: u.Ref, Field: u.Field})
	}
	return out
}

// UsesParents reports whether the rule follows a reference.
func (r *Rule) UsesParents() bool { return len(r.units) > 0 }

// UsesParents reports whether any rule of the set follows a reference.
func (s *Set) UsesParents() bool {
	if s == nil {
		return false
	}
	for _, r := range s.rules {
		if r.UsesParents() {
			return true
		}
	}
	return false
}

// ParentUses lists the references any rule of the set follows, once each.
func (s *Set) ParentUses() []ParentUse {
	if s == nil {
		return nil
	}
	var out []ParentUse
	seen := map[ParentUse]bool{}
	for _, op := range Ops {
		if r := s.rules[op]; r != nil {
			for _, u := range r.ParentUses() {
				if !seen[u] {
					seen[u] = true
					out = append(out, u)
				}
			}
		}
	}
	return out
}

// BindParents checks the units of every rule against the collections the references point to.
// It runs once the whole realm is loaded, because a reference can name any collection. A
// rule with a unit that isn't bound denies.
func (s *Set) BindParents(resolve func(ref string) (ParentTarget, bool)) []error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, op := range Ops {
		if r := s.rules[op]; r != nil {
			for _, e := range r.bindParents(resolve) {
				errs = append(errs, fmt.Errorf("%s: %w", r.Key, e))
			}
		}
	}
	return errs
}

func (r *Rule) bindParents(resolve func(ref string) (ParentTarget, bool)) []error {
	var errs []error
	for _, u := range r.units {
		t, ok := resolve(u.Ref)
		if !ok {
			errs = append(errs, fmt.Errorf("reference %q: unknown", u.Ref))
			continue
		}
		var problems []error
		add := func(format string, args ...any) {
			problems = append(problems, fmt.Errorf("parent via %q: "+format, append([]any{u.Ref}, args...)...))
		}
		checkPaths(u.Pred, t.Schema, func(format string, args ...any) {
			msg := strings.ReplaceAll(fmt.Sprintf(format, args...), "document", "parent")
			add("%s", strings.ReplaceAll(msg, "(not in schema.json)", "(not in the schema of the collection it points to)"))
		})
		walk(u.Pred, func(n ast.Node) {
			if p, ok := docPath(n); ok && t.Schema.EncryptedField != nil && t.Schema.EncryptedField(p) {
				add("parent.%s is encrypted; a comparison on a parent can't use encrypted fields", p)
			}
		})
		if len(problems) == 0 {
			if err := filterable(u.Pred, t.Schema); err != nil {
				problems = append(problems, fmt.Errorf("parent via %q: %s", u.Ref, strings.ReplaceAll(err.Error(), "document", "parent")))
			}
		}
		if len(problems) > 0 {
			errs = append(errs, problems...)
			continue
		}
		u.bound = true
	}
	return errs
}

// unitFilter is the filter, on the parent collection, of a unit for this caller.
func (r *Rule) unitFilter(u *parentUnit, env map[string]any) (storage.Filter, error) {
	if !u.bound {
		return nil, fmt.Errorf("parent(): reference %q isn't checked against its collection", u.Ref)
	}
	f, err := r.build(u.Pred, env)
	if err != nil {
		return nil, err
	}
	return storage.Simplify(f), nil
}

// buildParent turns a unit into a condition on the source field: the parents that satisfy
// it, resolved now, are the only ones a document may point to.
func (r *Rule) buildParent(idx int, env map[string]any) (storage.Filter, error) {
	u := r.units[idx]
	f, err := r.unitFilter(u, env)
	if err != nil {
		return nil, err
	}
	if c, ok := f.(storage.Const); ok && !bool(c) {
		return storage.Const(false), nil
	}
	ps, _ := env[parentsVar].(Parents)
	if ps == nil {
		return nil, errors.New("parent(): no access to the parent documents")
	}
	keys, err := ps.Keys(u.Ref, f)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return storage.Const(false), nil
	}
	return storage.Condition{Field: u.Field, Op: storage.OpIn, Value: keys}, nil
}

// parentHolds is the in-memory evaluation of a unit.
func (r *Rule) parentHolds(env map[string]any, idx int, key any) (bool, error) {
	if idx < 0 || idx >= len(r.units) {
		return false, errors.New("internal: unknown parent unit")
	}
	u := r.units[idx]
	if key == nil {
		return false, nil
	}
	f, err := r.unitFilter(u, env)
	if err != nil {
		return false, err
	}
	if c, ok := f.(storage.Const); ok && !bool(c) {
		return false, nil
	}
	ps, _ := env[parentsVar].(Parents)
	if ps == nil {
		return false, errors.New("parent(): no access to the parent documents")
	}
	doc, err := ps.Get(u.Ref, normalize(key))
	if err != nil || doc == nil {
		return false, err
	}
	return storage.Match(doc, f)
}

// parentOptions are the compile options of a rule with units: the hidden function.
func (r *Rule) parentOptions() []expr.Option {
	fn := func(params ...any) (any, error) {
		env, _ := params[0].(map[string]any)
		idx, _ := params[1].(int)
		return r.parentHolds(env, idx, params[2])
	}
	return []expr.Option{
		expr.Function(parentFunc, fn, new(func(map[string]any, int, any) bool)),
		expr.Patch(parentPatcher{}),
	}
}

// parentPatcher passes the evaluation environment to the hidden call.
type parentPatcher struct{}

func (parentPatcher) Visit(node *ast.Node) {
	if c, ok := (*node).(*ast.CallNode); ok {
		if id, ok := c.Callee.(*ast.IdentifierNode); ok && id.Value == parentFunc && len(c.Arguments) == 2 {
			c.Arguments = append([]ast.Node{&ast.IdentifierNode{Value: envVar}}, c.Arguments...)
		}
	}
}

// parentStub lets the rule as written compile (and type-check) before it is rewritten.
var parentStub = expr.Function("parent", func(...any) (any, error) {
	return nil, errors.New("internal: parent() was not rewritten")
}, new(func(any) map[string]any))
