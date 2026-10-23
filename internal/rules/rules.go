// Package rules checks and compiles a collection's rules (the `rules:` section of its collection.yaml): expr-lang
// expressions deciding, per operation, whether a caller may act on a
// whole document. Read rules also become backend-neutral storage filters.
// It knows nothing about HTTP or MongoDB.
package rules

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// Op is an operation a rule can allow.
type Op string

const (
	Read   Op = "read"
	Create Op = "create"
	Update Op = "update"
	Delete Op = "delete"
	// Restore and Purge are the operations of a collection that soft-deletes
	// (collection.yaml): restore decides who may read the deleted documents
	// and bring them back, purge who may remove them for good. Neither is
	// covered by `write`: they are denied unless declared.
	Restore Op = "restore"
	Purge   Op = "purge"
	// Invoke is a function's invoke rule (function.yaml): it decides who
	// may call the function and sees only `user`.
	Invoke Op = "invoke"
	// Config is the read rule of a path of the realm's app configuration
	// (app-config/rules.yaml): it sees `user` and `now`, with no document.
	Config Op = "config"
	// Reveal is the `reveal:` of an encrypted field: whether a caller who may read a document
	// sees the field's value or null. It sees `document` (the whole plaintext) and `user`, and is
	// evaluated per document, in memory.
	Reveal Op = "reveal"
	// Trigger is the `when:` of a collection's trigger (collection.yaml): it sees the
	// document before and after a change, `changed()`, `now` (the change's time) and the
	// app configuration — and no caller, because an event has none.
	Trigger Op = "trigger"
)

// Ops lists the operations in a fixed order.
var Ops = []Op{Read, Create, Update, Delete, Restore, Purge}

// User is the signed-in user as rules see it; nil for anonymous callers.
type User struct {
	ID            string   `expr:"id"`
	Email         string   `expr:"email"`
	EmailVerified bool     `expr:"email_verified"`
	Roles         []string `expr:"roles"`
	// MFA says the session used a second factor; Methods are how it was created: the
	// first factor and, with MFA, "totp" or "recovery". API keys are ["key"].
	MFA     bool     `expr:"mfa"`
	Methods []string `expr:"methods"`
	// Consents maps each consent the user holds to the version they accepted.
	Consents map[string]string `expr:"consents"`
	// Orgs are the memberships that count, by organization id (design/organizations.md §6):
	// filled in only for a rule that uses them.
	Orgs map[string]*UserOrg `expr:"orgs"`
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
	// DateField reports whether a path is stored as a date (x-backd-store:
	// date): its value is a time in rules, comparable with `now`. May be nil.
	DateField func(path string) bool
	// EncryptedField reports whether a document path is stored encrypted: a read rule, which is
	// applied inside the query, can't compare it. May be nil.
	EncryptedField func(path string) bool
	// SearchableField reports whether an encrypted path has a blind index: a read rule may then
	// compare it with == or `in`, nothing else. May be nil.
	SearchableField func(path string) bool
	// Roles are the role names declared in realm.yaml.
	Roles []string
	// Consents are the consent names declared in realm.yaml, for hasConsent.
	Consents []string
	// ConfigLeaf reports whether a dot path is a value of the realm's app
	// configuration, for appConfig; nil when the realm declares none.
	ConfigLeaf func(path string) bool
	// OrgRoles are the organization roles declared in realm.yaml, for hasOrgRole; nil when
	// the realm doesn't enable organizations.
	OrgRoles []string
	// InOrgs says the collection's documents can belong to an organization, so rules may name
	// document._meta.org (and data._meta.org on create).
	InOrgs bool
	// TeamRoles are the team roles declared in realm.yaml, for hasTeamRole.
	TeamRoles []string
	// InTeams says the collection's documents can belong to a team, so rules may name
	// document._meta.team (and data._meta.team on create).
	InTeams bool
}

// Set is a collection's compiled rules. A nil rule denies the operation.
type Set struct {
	File  string
	rules map[Op]*Rule
}

// Rule is one compiled rule.
type Rule struct {
	Op     Op
	Key    string // the rules key it came from: the op, or "write"
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

// Parse compiles and checks the `rules:` section of a collection's collection.yaml: a
// mapping of operation (read, create, update, delete, restore, purge, or write for the
// three that change a document) to an expression.
func Parse(doc map[string]string, schema Schema) (*Set, []error) {
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if k != "write" && !slices.Contains(Ops, Op(k)) {
			errs = append(errs, fmt.Errorf("unknown key %q: want read, create, update, delete, restore, purge or write", k))
		}
	}
	s := &Set{rules: map[Op]*Rule{}}
	for _, op := range Ops {
		key := string(op)
		src, ok := doc[key]
		if !ok && (op == Create || op == Update || op == Delete) {
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
		"before":   map[string]any{},
		"data":     map[string]any{},
		"now":      time.Time{},
		"changed":  func() []string { return nil },
		"config":   map[string]any{},
		orgsVar:    map[string]*UserOrg{},
	}
}

// appConfig reads a value of the app configuration. Rules write appConfig("a.b");
// compilation passes the request's configuration as the first argument, so the
// value is fixed for the request and is a constant to the filter pushdown.
func appConfig(params ...any) (any, error) {
	cfg, _ := params[0].(map[string]any)
	return cfg[params[1].(string)], nil
}

var appConfigFunc = expr.Function("appConfig", appConfig, new(func(map[string]any, string) any))

// configPatcher rewrites appConfig("a.b") into appConfig(config, "a.b").
type configPatcher struct{}

func (configPatcher) Visit(node *ast.Node) {
	c, ok := (*node).(*ast.CallNode)
	if !ok || len(c.Arguments) != 1 {
		return
	}
	if id, ok := c.Callee.(*ast.IdentifierNode); ok && id.Value == "appConfig" {
		c.Arguments = []ast.Node{&ast.IdentifierNode{Value: "config"}, c.Arguments[0]}
	}
}

// compileOptions are what every rule is compiled with, whole or in parts.
func compileOptions(extra ...expr.Option) []expr.Option {
	opts := []expr.Option{expr.Env(compileEnv()), hasRoleFunc, hasConsentFunc, appConfigFunc, expr.Patch(configPatcher{}), expr.Patch(orgPatcher{})}
	opts = append(opts, orgFuncs...)
	return append(opts, extra...)
}

// UsesConfig reports whether the rule reads the app configuration.
func (r *Rule) UsesConfig() bool {
	found := false
	walk(r.tree, func(n ast.Node) {
		if c, ok := n.(*ast.CallNode); ok {
			if id, ok := c.Callee.(*ast.IdentifierNode); ok && id.Value == "appConfig" {
				found = true
			}
		}
	})
	return found
}

// UsesConfig reports whether any rule of the set reads the app configuration.
func (s *Set) UsesConfig() bool {
	if s == nil {
		return false
	}
	for _, r := range s.rules {
		if r.UsesConfig() {
			return true
		}
	}
	return false
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

// hasConsent is true when user holds any version of every consent named; false for
// anonymous callers.
func hasConsent(params ...any) (any, error) {
	u, _ := params[0].(*User)
	if u == nil {
		return false, nil
	}
	for _, c := range params[1:] {
		if u.Consents[c.(string)] == "" {
			return false, nil
		}
	}
	return true, nil
}

var hasConsentFunc = expr.Function("hasConsent", hasConsent, new(func(*User, ...string) bool))

// CompileInvoke compiles and checks a function's invoke rule. It may use
// `user`, hasRole with the realm's roles and appConfig with the values
// configLeaf says exist (nil when the realm declares no app configuration);
// there is no document.
func CompileInvoke(src string, roles []string, configLeaf func(string) bool) (*Rule, error) {
	none := func(string) bool { return false }
	return compile(Invoke, string(Invoke), src, Schema{DocumentField: none, DataField: none, ScalarField: none, ArrayField: none, Roles: roles, ConfigLeaf: configLeaf})
}

// CompileConfig compiles and checks the read rule of a path of the app
// configuration. It may use `user`, `now`, hasRole with the realm's roles and
// hasConsent with its consents; there is no document.
func CompileConfig(src string, roles, consents []string) (*Rule, error) {
	none := func(string) bool { return false }
	return compile(Config, "read", src, Schema{DocumentField: none, DataField: none, ScalarField: none, ArrayField: none, Roles: roles, Consents: consents})
}

// AlwaysTrue reports whether the rule is literally `true`: it allows everyone,
// with or without a session, so what it guards can be cached publicly.
func (r *Rule) AlwaysTrue() bool {
	b, ok := r.tree.(*ast.BoolNode)
	return ok && b.Value
}

// AlwaysFalse reports whether the rule is literally `false`: nobody is ever allowed.
func (r *Rule) AlwaysFalse() bool {
	b, ok := r.tree.(*ast.BoolNode)
	return ok && !b.Value
}

// CompileReveal compiles and checks the `reveal:` of an encrypted field. It may use `document`,
// `user`, `now`, hasRole and the organization helpers like an update rule, but not `data`.
func CompileReveal(src string, schema Schema) (*Rule, error) {
	return compile(Reveal, string(Reveal), src, schema)
}

// CompileTrigger compiles and checks the `when:` of a trigger. It may use `document` (after
// the change), `before` (before it), `changed()`, `now` and appConfig; `user`, `data` and the
// helpers that need a caller are errors, because an event has none.
func CompileTrigger(src string, schema Schema) (*Rule, error) {
	return compile(Trigger, string(Trigger), src, schema)
}

// References reports whether the rule's source names the variable (`document`, `before`).
func (r *Rule) References(name string) bool { return refs(r.tree, name) }

// Fires evaluates a trigger's `when:` for one change. before and after are the documents in
// API shape, nil when there is none (a created document has no before, a removed one no
// after); at is the change's cluster time and config the app configuration, for appConfig.
//
// A missing document reads as an empty one, so `before.status != 'paid'` is true for a
// creation (it came from nothing). The exception is a removal: a condition that names
// `document` never matches it, whatever it says, because the document is gone; conditions
// meant for removals read `before`.
func (r *Rule) Fires(before, after map[string]any, at time.Time, config map[string]any) (bool, error) {
	if after == nil && r.References("document") {
		return false, nil
	}
	if before == nil {
		before = map[string]any{}
	}
	doc := after
	if doc == nil {
		doc = map[string]any{}
	}
	return r.Allow(Values{Document: doc, Before: before, Event: true, Now: at, Config: config})
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
	prog, err := expr.Compile(src, compileOptions(expr.AsBool())...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	r := &Rule{Op: op, Key: key, Source: src, prog: prog, tree: tree.Node}
	if err := check(r, schema); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if op == Read || op == Restore {
		if err := r.prepareFilter(); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
	}
	return r, nil
}
