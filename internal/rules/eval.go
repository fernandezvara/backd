package rules

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"

	"github.com/fernandezvara/backd/internal/storage"
)

// Values are what a rule is evaluated with.
type Values struct {
	User     *User          // nil for anonymous callers
	Document map[string]any // stored document (read, update, delete)
	Data     map[string]any // incoming document (create, update)
	Now      time.Time
	// Orgs are the caller's memberships that count, for the organization helpers; only filled
	// in when a rule reads them (Rule.UsesOrgs).
	Orgs map[string]*UserOrg
	// Before is the document before the change, and Event says this is a trigger's `when:`:
	// `changed()` then compares Before with Document (the document after).
	Before map[string]any
	Event  bool
	// Config is the app configuration, by dot path of each value, for appConfig();
	// only filled in when a rule reads it.
	Config map[string]any
}

func (v Values) env() map[string]any {
	diff := func() []string { return changed(v.Document, v.Data) }
	if v.Event {
		diff = func() []string { return changed(v.Before, v.Document) }
	}
	before := v.Before
	if before == nil {
		before = map[string]any{}
	}
	return map[string]any{
		"user":     v.User,
		"document": v.Document,
		"before":   before,
		"data":     v.Data,
		"now":      v.Now,
		"changed":  diff,
		"config":   v.configMap(),
		orgsVar:    v.orgsMap(),
	}
}

// Allow evaluates the rule. An evaluation error (for example comparing
// values of different types) is returned, and must be treated as a denial.
func (r *Rule) Allow(v Values) (bool, error) {
	out, err := expr.Run(r.prog, v.env())
	if err != nil {
		return false, err
	}
	ok, _ := out.(bool)
	return ok, nil
}

// ChangedFields lists the top-level user fields whose values differ between two
// versions of a document: what `changed()` returns in a rule, and what an event
// carries as `changed`.
func ChangedFields(before, after map[string]any) []string { return changed(before, after) }

// changed lists the top-level user fields whose values differ between the
// stored document and the new one.
func changed(before, after map[string]any) []string {
	var out []string
	seen := map[string]bool{"id": true, "_id": true, "_meta": true}
	for _, m := range []map[string]any{before, after} {
		for k := range m {
			if seen[k] {
				continue
			}
			seen[k] = true
			if !reflect.DeepEqual(before[k], after[k]) {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out)
	return out
}

// prepareFilter compiles every document-independent part of a read rule,
// to be evaluated once per request by Filter.
func (r *Rule) prepareFilter() error {
	r.consts = map[ast.Node]*vm.Program{}
	var err error
	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		if err != nil {
			return
		}
		if !refs(n, "document") {
			r.consts[n], err = expr.Compile(n.String(), compileOptions()...)
			return
		}
		cs := children(n)
		if _, call, ok := orgCall(n); ok {
			cs = call.Arguments // the callee is a function, not a value
		}
		for _, c := range cs {
			visit(c)
		}
	}
	visit(r.tree)
	return err
}

// Filter turns a read rule into a storage filter for the given caller. The
// result is simplified: Const(true) allows every document, Const(false)
// none, whatever they contain.
func (r *Rule) Filter(v Values) (storage.Filter, error) {
	f, err := r.build(r.tree, v.env())
	if err != nil {
		return nil, err
	}
	return storage.Simplify(f), nil
}

func (r *Rule) value(n ast.Node, env map[string]any) (any, error) {
	prog, ok := r.consts[n]
	if !ok {
		return nil, fmt.Errorf("internal: no program for %s", n.String())
	}
	out, err := expr.Run(prog, env)
	if err != nil {
		return nil, err
	}
	return normalize(out), nil
}

// flipped turns `value op field` into `field op' value`.
var flipped = map[string]string{"==": "==", "!=": "!=", "<": ">", "<=": ">=", ">": "<", ">=": "<="}

var operators = map[string]storage.Operator{
	"==": storage.OpEq, "!=": storage.OpNe, "<": storage.OpLt, "<=": storage.OpLte, ">": storage.OpGt, ">=": storage.OpGte,
}

func (r *Rule) build(n ast.Node, env map[string]any) (storage.Filter, error) {
	if !refs(n, "document") {
		v, err := r.value(n, env)
		if err != nil {
			return nil, err
		}
		b, _ := v.(bool)
		return storage.Const(b), nil
	}
	orgs, _ := env[orgsVar].(map[string]*UserOrg)
	switch n := n.(type) {
	case *ast.UnaryNode:
		inner, err := r.build(n.Node, env)
		if err != nil {
			return nil, err
		}
		return storage.Not{Filter: inner}, nil
	case *ast.CallNode:
		if name, c, ok := orgCall(n); ok {
			return r.buildOrgCall(name, c, orgs)
		}
	case *ast.MemberNode, *ast.ChainNode:
		path, _ := docPath(n)
		return storage.Condition{Field: path, Op: storage.OpEq, Value: true}, nil
	case *ast.BinaryNode:
		switch n.Operator {
		case "&&", "and", "||", "or":
			and := n.Operator == "&&" || n.Operator == "and"
			l, err := r.build(n.Left, env)
			if err != nil {
				return nil, err
			}
			// Short-circuit like expr does: the right side may rely on a
			// guard on the left (`user != nil && …`).
			if c, ok := storage.Simplify(l).(storage.Const); ok && bool(c) != and {
				return c, nil
			}
			rt, err := r.build(n.Right, env)
			if err != nil {
				return nil, err
			}
			if and {
				return storage.And{l, rt}, nil
			}
			return storage.Or{l, rt}, nil
		case "in":
			if path, ok := claimRef(n.Left); ok {
				v, err := r.value(n.Right, env)
				if err != nil {
					return nil, err
				}
				return r.buildClaim(path, "in", v, orgs), nil
			}
			if path, ok := claimRef(n.Right); ok {
				v, err := r.value(n.Left, env)
				if err != nil {
					return nil, err
				}
				return r.buildClaim(path, "has", v, orgs), nil
			}
			if path, ok := docPath(n.Left); ok {
				v, err := r.value(n.Right, env)
				if err != nil {
					return nil, err
				}
				list, ok := v.([]any)
				if !ok {
					return storage.Const(false), nil
				}
				return storage.Condition{Field: path, Op: storage.OpIn, Value: list}, nil
			}
			path, _ := docPath(n.Right)
			v, err := r.value(n.Left, env)
			if err != nil || v == nil {
				return storage.Const(false), err
			}
			// Equality on an array field matches any of its elements.
			return storage.Condition{Field: path, Op: storage.OpEq, Value: v}, nil
		default:
			op, fieldNode, valueNode := n.Operator, n.Left, n.Right
			if path, ok := claimRef(n.Left); ok {
				v, err := r.value(n.Right, env)
				if err != nil {
					return nil, err
				}
				return r.buildClaim(path, op, v, orgs), nil
			}
			if path, ok := claimRef(n.Right); ok {
				v, err := r.value(n.Left, env)
				if err != nil {
					return nil, err
				}
				return r.buildClaim(path, flipped[op], v, orgs), nil
			}
			if _, ok := docPath(n.Left); !ok {
				op, fieldNode, valueNode = flipped[op], n.Right, n.Left
			}
			path, _ := docPath(fieldNode)
			v, err := r.value(valueNode, env)
			if err != nil {
				return nil, err
			}
			if v == nil {
				switch op {
				case "==":
					return storage.Condition{Field: path, Op: storage.OpIsNull, Value: true}, nil
				case "!=":
					return storage.Condition{Field: path, Op: storage.OpIsNull, Value: false}, nil
				}
				return storage.Const(false), nil
			}
			return storage.Condition{Field: path, Op: operators[op], Value: v}, nil
		}
	}
	return nil, fmt.Errorf("internal: not a filter: %s", n.String())
}

// normalize converts expr results to the value types storage expects:
// int64 and float64 numbers, []any lists, UTC times.
func normalize(v any) any {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	case time.Time:
		return t.UTC()
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalize(e)
		}
		return out
	}
	return v
}

func (v Values) configMap() map[string]any {
	if v.Config == nil {
		return map[string]any{}
	}
	return v.Config
}

func (v Values) orgsMap() map[string]*UserOrg {
	if v.Orgs == nil {
		return map[string]*UserOrg{}
	}
	return v.Orgs
}
