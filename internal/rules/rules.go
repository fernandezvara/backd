// Package rules loads and checks a collection's rules.yaml: expr-lang
// expressions deciding, per operation, whether a caller may act on a
// whole document. Read rules also become backend-neutral storage filters.
// It knows nothing about HTTP or MongoDB.
package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
	"go.yaml.in/yaml/v3"
)

// Op is an operation a rule can allow.
type Op string

const (
	Read   Op = "read"
	Create Op = "create"
	Update Op = "update"
	Delete Op = "delete"
	// Invoke is a function's invoke rule (function.yaml): it decides who
	// may call the function and sees only `user`.
	Invoke Op = "invoke"
)

// Ops lists the operations in a fixed order.
var Ops = []Op{Read, Create, Update, Delete}

// User is the signed-in user as rules see it; nil for anonymous callers.
type User struct {
	ID            string   `expr:"id"`
	Email         string   `expr:"email"`
	EmailVerified bool     `expr:"email_verified"`
	Roles         []string `expr:"roles"`
}

// Schema describes what the rules of one collection may refer to.
type Schema struct {
	// DocumentField reports whether a dot path exists on stored documents
	// (schema fields plus id and _meta fields).
	DocumentField func(path string) bool
	// DataField reports whether a dot path exists in request bodies.
	DataField func(path string) bool
	// ScalarField reports whether a document path holds a single value:
	// declared with types that don't include "array", and not reached
	// through an array. Read rules may only compare such fields, because
	// MongoDB compares arrays element by element.
	ScalarField func(path string) bool
	// ArrayField reports whether a document path is declared as an array
	// (and not reached through another array), for `value in document.path`.
	ArrayField func(path string) bool
	// Roles are the role names declared in realm.yaml.
	Roles []string
}

// Set is a collection's compiled rules. A nil rule denies the operation.
type Set struct {
	File  string
	rules map[Op]*Rule
}

// Rule is one compiled rule.
type Rule struct {
	Op     Op
	Key    string // the rules.yaml key it came from: the op, or "write"
	Source string
	prog   *vm.Program
	tree   ast.Node
	consts map[ast.Node]*vm.Program // read rules: document-independent parts
}

// For returns the rule of op, or nil if none allows it.
func (s *Set) For(op Op) *Rule {
	if s == nil {
		return nil
	}
	return s.rules[op]
}

// Load reads and checks path. A missing file returns (nil, nil): no rule
// allows anything.
func Load(path string, schema Schema) (*Set, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s, errs := Parse(data, schema)
	if len(errs) > 0 {
		for i, e := range errs {
			errs[i] = fmt.Errorf("%s: %w", path, e)
		}
		return nil, errors.Join(errs...)
	}
	s.File = path
	return s, nil
}

// Parse compiles and checks rules.yaml content.
func Parse(data []byte, schema Schema) (*Set, []error) {
	var doc map[string]string
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, []error{fmt.Errorf("invalid YAML: want a mapping of operation to expression string: %w", err)}
	}
	var errs []error
	for k := range doc {
		if k != "write" && !slices.Contains(Ops, Op(k)) {
			errs = append(errs, fmt.Errorf("unknown key %q: want read, create, update, delete or write", k))
		}
	}
	s := &Set{rules: map[Op]*Rule{}}
	for _, op := range Ops {
		key := string(op)
		src, ok := doc[key]
		if !ok && op != Read {
			key, src, ok = "write", doc["write"], doc["write"] != ""
		}
		if !ok {
			continue
		}
		r, err := compile(op, key, src, schema)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.rules[op] = r
	}
	return s, errs
}

// compileEnv is the variable environment rules are type-checked against.
func compileEnv() map[string]any {
	return map[string]any{
		"user":     (*User)(nil),
		"document": map[string]any{},
		"data":     map[string]any{},
		"now":      time.Time{},
		"changed":  func() []string { return nil },
	}
}

// hasRole is true when user has any of roles; false for anonymous callers.
func hasRole(params ...any) (any, error) {
	u, _ := params[0].(*User)
	if u == nil {
		return false, nil
	}
	for _, r := range params[1:] {
		if slices.Contains(u.Roles, r.(string)) {
			return true, nil
		}
	}
	return false, nil
}

var hasRoleFunc = expr.Function("hasRole", hasRole, new(func(*User, ...string) bool))

// CompileInvoke compiles and checks a function's invoke rule. It may use
// `user` and hasRole with the realm's roles; there is no document.
func CompileInvoke(src string, roles []string) (*Rule, error) {
	none := func(string) bool { return false }
	return compile(Invoke, string(Invoke), src, Schema{DocumentField: none, DataField: none, ScalarField: none, ArrayField: none, Roles: roles})
}

func compile(op Op, key, src string, schema Schema) (*Rule, error) {
	where := key
	if key != string(op) {
		where = fmt.Sprintf("write (used for %s)", op)
	}
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("%s: empty rule", where)
	}
	tree, err := parser.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	prog, err := expr.Compile(src, expr.Env(compileEnv()), hasRoleFunc, expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	r := &Rule{Op: op, Key: key, Source: src, prog: prog, tree: tree.Node}
	if err := check(r, schema); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if op == Read {
		if err := r.prepareFilter(); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
	}
	return r, nil
}
