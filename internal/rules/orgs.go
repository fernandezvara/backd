package rules

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"

	"github.com/fernandezvara/backd/internal/storage"
)

// Organization helpers (design/organizations.md §6.2):
//
//	inOrg(document._meta.org)
//	hasOrgRole(document._meta.org, "editor")        // or a role named by the document
//	orgClaims(document._meta.org).plan == "pro"
//
// They see the caller's memberships that count, which the HTTP layer loads once per request
// into Values.Orgs when a rule uses them. Like appConfig, each call is rewritten to receive
// them as its first argument. In read rules they become conditions on `_meta.org`, so lists
// and counts only show what the caller may see.

// UserOrg is one membership that counts: the roles the caller holds in the organization
// (direct, default and through teams), the teams, the team roles held within them and the
// organization's claims.
type UserOrg struct {
	Roles []string `expr:"roles"`
	Teams []string `expr:"teams"`
	// TeamRoles are the team roles held in force, by team id.
	TeamRoles map[string][]string `expr:"team_roles"`
	Claims    map[string]any      `expr:"claims"`
}

// OrgsEnv is the memberships by organization id.
type OrgsEnv = map[string]*UserOrg

const orgsVar = "orgs"

func orgID(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func inOrg(params ...any) (any, error) {
	orgs, _ := params[0].(OrgsEnv)
	id, ok := orgID(params[1])
	if !ok {
		return false, nil
	}
	_, member := orgs[id]
	return member, nil
}

func hasOrgRole(params ...any) (any, error) {
	orgs, _ := params[0].(OrgsEnv)
	id, ok := orgID(params[1])
	role, isStr := params[2].(string)
	if !ok || !isStr {
		return false, nil
	}
	m := orgs[id]
	return m != nil && slices.Contains(m.Roles, role), nil
}

func orgClaims(params ...any) (any, error) {
	orgs, _ := params[0].(OrgsEnv)
	if id, ok := orgID(params[1]); ok {
		if m := orgs[id]; m != nil && m.Claims != nil {
			return m.Claims, nil
		}
	}
	return map[string]any{}, nil
}

var orgFuncs = []expr.Option{
	expr.Function("inOrg", inOrg, new(func(OrgsEnv, any) bool)),
	expr.Function("hasOrgRole", hasOrgRole, new(func(OrgsEnv, any, any) bool)),
	expr.Function("orgClaims", orgClaims, new(func(OrgsEnv, any) map[string]any)),
}

// orgArgs says how many arguments each helper takes as written.
var orgArgs = map[string]int{"inOrg": 1, "hasOrgRole": 2, "orgClaims": 1}

// orgPatcher rewrites inOrg(x) into inOrg(orgs, x), and the others alike. A comparison with a
// claim, which only means something for an organization the caller belongs to, becomes
// `inOrg(x) && comparison`: for the others the comparison is false, as the query applies it.
type orgPatcher struct{}

func (orgPatcher) Visit(node *ast.Node) {
	switch n := (*node).(type) {
	case *ast.CallNode:
		id, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			return
		}
		if want, known := orgArgs[id.Value]; known && len(n.Arguments) == want {
			n.Arguments = append([]ast.Node{&ast.IdentifierNode{Value: orgsVar}}, n.Arguments...)
		}
	case *ast.BinaryNode:
		if !comparisons[n.Operator] && n.Operator != "in" {
			return
		}
		for _, side := range []ast.Node{n.Left, n.Right} {
			if c := patchedClaimCall(side); c != nil {
				guard := &ast.CallNode{
					Callee:    &ast.IdentifierNode{Value: "inOrg"},
					Arguments: []ast.Node{&ast.IdentifierNode{Value: orgsVar}, c.Arguments[len(c.Arguments)-1]},
				}
				ast.Patch(node, &ast.BinaryNode{Operator: "&&", Left: guard, Right: n})
				return
			}
		}
	}
}

// patchedClaimCall returns the orgClaims call at the bottom of a member chain (after its
// children were patched, so with the memberships as its first argument).
func patchedClaimCall(n ast.Node) *ast.CallNode {
	if c, isChain := n.(*ast.ChainNode); isChain {
		n = c.Node
	}
	steps := 0
	for {
		m, ok := n.(*ast.MemberNode)
		if !ok {
			break
		}
		n = m.Node
		steps++
	}
	c, ok := n.(*ast.CallNode)
	if !ok || steps == 0 {
		return nil
	}
	if id, ok := c.Callee.(*ast.IdentifierNode); !ok || id.Value != "orgClaims" {
		return nil
	}
	return c
}

func orgCall(n ast.Node) (name string, c *ast.CallNode, ok bool) {
	c, ok = n.(*ast.CallNode)
	if !ok {
		return "", nil, false
	}
	id, ok := c.Callee.(*ast.IdentifierNode)
	if !ok {
		return "", nil, false
	}
	_, known := orgArgs[id.Value]
	return id.Value, c, known
}

// UsesOrgs reports whether the rule reads the caller's memberships: a helper or `user.orgs`.
func (r *Rule) UsesOrgs() bool {
	found := false
	walk(r.tree, func(n ast.Node) {
		if _, _, ok := orgCall(n); ok {
			found = true
		}
		if m, ok := n.(*ast.MemberNode); ok && isUserField(m, "orgs") {
			found = true
		}
		if m, ok := n.(*ast.MemberNode); ok {
			// user.orgs[...] and user.orgs.x: the chain starts at user.orgs
			if root, path, ok := memberPath(m); ok && root == "user" && (path == "orgs" || strings.HasPrefix(path, "orgs.")) {
				found = true
			}
		}
	})
	return found
}

// UsesOrgs reports whether any rule of the set reads memberships.
func (s *Set) UsesOrgs() bool {
	if s == nil {
		return false
	}
	for _, r := range s.rules {
		if r.UsesOrgs() {
			return true
		}
	}
	return false
}

// claimRef recognizes orgClaims(document._meta.org).a.b and returns the claim path.
func claimRef(n ast.Node) (path []string, ok bool) {
	if c, isChain := n.(*ast.ChainNode); isChain {
		n = c.Node
	}
	var parts []string
	for {
		m, isMember := n.(*ast.MemberNode)
		if !isMember {
			break
		}
		s, isStr := m.Property.(*ast.StringNode)
		if !isStr || m.Method {
			return nil, false
		}
		parts = append(parts, s.Value)
		n = m.Node
	}
	name, c, isCall := orgCall(n)
	if !isCall || name != "orgClaims" || len(parts) == 0 || len(c.Arguments) != 1 {
		return nil, false
	}
	slices.Reverse(parts)
	return parts, true
}

// claimOrg is the argument of the orgClaims call at the bottom of a claim reference.
func claimCall(n ast.Node) *ast.CallNode {
	if c, isChain := n.(*ast.ChainNode); isChain {
		n = c.Node
	}
	for {
		m, ok := n.(*ast.MemberNode)
		if !ok {
			break
		}
		n = m.Node
	}
	c, _ := n.(*ast.CallNode)
	return c
}

func claimValue(claims map[string]any, path []string) (any, bool) {
	var cur any = claims
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func orgField(arg ast.Node) bool {
	p, ok := docPath(arg)
	return ok && p == "_meta.org"
}

// orgIDs lists, in a fixed order, the organizations where pred holds.
func orgIDs(orgs OrgsEnv, pred func(*UserOrg) bool) []any {
	ids := make([]string, 0, len(orgs))
	for id, m := range orgs {
		if pred(m) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func inList(ids []any) storage.Filter {
	if len(ids) == 0 {
		return storage.Const(false)
	}
	return storage.Condition{Field: "_meta.org", Op: storage.OpIn, Value: ids}
}

// buildOrgCall turns a helper call that reads the document into a filter.
func (r *Rule) buildOrgCall(name string, c *ast.CallNode, orgs OrgsEnv) (storage.Filter, error) {
	if len(c.Arguments) == 0 || !orgField(c.Arguments[0]) {
		return nil, fmt.Errorf("internal: %s must be called with document._meta.org", name)
	}
	switch name {
	case "inOrg":
		return inList(orgIDs(orgs, func(*UserOrg) bool { return true })), nil
	case "hasOrgRole":
		if len(c.Arguments) != 2 {
			break
		}
		if s, ok := c.Arguments[1].(*ast.StringNode); ok {
			return inList(orgIDs(orgs, func(m *UserOrg) bool { return slices.Contains(m.Roles, s.Value) })), nil
		}
		// A role named by the document: one (organization, role) pair at a time.
		field, ok := docPath(c.Arguments[1])
		if !ok {
			break
		}
		var pairs storage.Or
		for _, id := range slices.Sorted(mapKeys(orgs)) {
			for _, role := range orgs[id].Roles {
				pairs = append(pairs, storage.And{
					storage.Condition{Field: "_meta.org", Op: storage.OpEq, Value: id},
					storage.Condition{Field: field, Op: storage.OpEq, Value: role},
				})
			}
		}
		if len(pairs) == 0 {
			return storage.Const(false), nil
		}
		return pairs, nil
	}
	return nil, fmt.Errorf("internal: not a filter: %s", c.String())
}

func mapKeys(m OrgsEnv) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// buildClaim turns `orgClaims(document._meta.org).path <op> value` into a filter: the
// organizations whose claim satisfies it.
func (r *Rule) buildClaim(path []string, op string, value any, orgs OrgsEnv) storage.Filter {
	return inList(orgIDs(orgs, func(m *UserOrg) bool {
		got, ok := claimValue(m.Claims, path)
		return claimHolds(got, ok, op, value)
	}))
}

func claimHolds(got any, present bool, op string, want any) bool {
	got, want = normalize(got), normalize(want)
	switch op {
	case "==":
		if want == nil {
			return !present || got == nil
		}
		return present && equalClaim(got, want)
	case "!=":
		if want == nil {
			return present && got != nil
		}
		return !present || !equalClaim(got, want)
	case "in":
		list, _ := want.([]any)
		return present && slices.ContainsFunc(list, func(e any) bool { return equalClaim(got, e) })
	case "has": // want is an element of the claim's array
		list, _ := got.([]any)
		return present && slices.ContainsFunc(list, func(e any) bool { return equalClaim(e, want) })
	}
	a, aok := number(got)
	b, bok := number(want)
	if aok && bok {
		switch op {
		case "<":
			return a < b
		case "<=":
			return a <= b
		case ">":
			return a > b
		case ">=":
			return a >= b
		}
	}
	sa, aStr := got.(string)
	sb, bStr := want.(string)
	if aStr && bStr {
		switch op {
		case "<":
			return sa < sb
		case "<=":
			return sa <= sb
		case ">":
			return sa > sb
		case ">=":
			return sa >= sb
		}
	}
	return false
}

func equalClaim(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x == y
	}
	return reflect.DeepEqual(a, b)
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}
