package rules

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

// itemSchema is a child collection whose list_id references lists.
func itemSchema() Schema {
	s := testSchema
	s.DocumentField = func(p string) bool { return p == "list_id" || p == "done" || testSchema.DocumentField(p) }
	s.DataField = func(p string) bool { return p == "list_id" || p == "done" || testSchema.DataField(p) }
	s.ScalarField = func(p string) bool { return p == "list_id" || p == "done" || testSchema.ScalarField(p) }
	s.ParentRef = func(p string) (ParentRef, bool) {
		switch p {
		case "list_id":
			return ParentRef{Name: "list"}, true
		case "tags":
			return ParentRef{Name: "tags", Many: true}, true
		}
		return ParentRef{}, false
	}
	return s
}

// listSchema is the parent collection.
func listSchema() Schema {
	in := func(ps ...string) func(string) bool { return func(p string) bool { return slices.Contains(ps, p) } }
	return Schema{
		DocumentField:  in("id", "members", "public", "owner", "name", "_meta.owner"),
		DataField:      in("members", "public", "owner", "name"),
		ScalarField:    in("id", "public", "owner", "name", "_meta.owner"),
		ArrayField:     in("members"),
		EncryptedField: in("secret"),
	}
}

func bindLists(s *Set) []error {
	return s.BindParents(func(ref string) (ParentTarget, bool) {
		return ParentTarget{Schema: listSchema()}, ref == "list"
	})
}

func parseParents(t *testing.T, src string) (*Set, []error) {
	t.Helper()
	s, errs := parseYAML(src, itemSchema())
	if len(errs) == 0 {
		errs = bindLists(s)
	}
	return s, errs
}

func TestParentRulesAccepted(t *testing.T) {
	for _, src := range []string{
		"read: user != nil && user.id in parent(document.list_id).members",
		"create: user != nil && user.id in parent(data.list_id).members",
		"update: user != nil && user.id in parent(document.list_id).members && user.id in parent(data.list_id).members",
		"read: parent(document.list_id).public == true || (user != nil && user.id in parent(document.list_id).members)",
		"read: user != nil && !(parent(document.list_id).owner == user.id)",
		"read: user != nil && (parent(document.list_id).owner == user.id || document._meta.owner == user.id)",
		"create: user != nil && parent(data.list_id).owner == user.id",
		"delete: user != nil && parent(document.list_id).owner == user.id",
		"read: hasRole(user, 'admin') || (user != nil && user.id in parent(document.list_id).members)",
	} {
		if _, errs := parseParents(t, src); len(errs) > 0 {
			t.Errorf("%q: %v", src, errs)
		}
	}
}

func TestParentRulesRefused(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"not a reference field", "read: user != nil && user.id in parent(document.title).members", `"title" isn't the source field of a reference`},
		{"many", "read: user != nil && user.id in parent(document.tags).members", "many: true"},
		{"bare parent", "read: parent(document.list_id) != nil", "must be followed by a field name"},
		{"literal names", "read: user != nil && user.id in parent(document.list_id[0]).members", "literal names"},
		{"other side reads the document", "read: parent(document.list_id).owner == document._meta.owner", "can't depend on the document"},
		{"other side reads data", "update: parent(document.list_id).owner == data.title", "can't depend on the document"},
		{"two parents", "update: user != nil && parent(document.list_id).owner == user.id && parent(data.list_id).owner == user.id && (parent(data.list_id).public == true || parent(document.list_id).public == true)", ""},
		{"mixed in one term", "update: parent(document.list_id).name == parent(data.list_id).name", "one parent"},
		{"unguarded user", "read: user.id in parent(document.list_id).members", "needs a guard"},
		{"unknown parent field", "read: parent(document.list_id).nope == 'x'", "unknown field parent.nope"},
		{"array compared", "read: parent(document.list_id).members == 'x'", "is an array"},
		{"computed parent field", "read: len(parent(document.list_id).name) > 2", "read rules must be database filters"},
		{"data on read", "read: user != nil && user.id in parent(data.list_id).members", "`data` is only available"},
		{"document on create", "create: user != nil && user.id in parent(document.list_id).members", "not available on create"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, errs := parseParents(t, c.src)
			if c.want == "" {
				return // accepted or not: covered by TestParentRulesAccepted
			}
			if len(errs) == 0 {
				t.Fatalf("%q accepted", c.src)
			}
			if !strings.Contains(fmt.Sprint(errs), c.want) {
				t.Fatalf("%q: %v, want %q", c.src, errs, c.want)
			}
		})
	}
}

func TestParentRulesNeedABoundCollection(t *testing.T) {
	s, errs := parseYAML("read: user != nil && user.id in parent(document.list_id).members", itemSchema())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := s.For(Read).Filter(Values{User: &User{ID: "u1"}}); err == nil {
		t.Fatal("an unbound parent rule built a filter")
	}
	if errs := s.BindParents(func(string) (ParentTarget, bool) { return ParentTarget{}, false }); len(errs) == 0 {
		t.Fatal("unknown reference accepted")
	}
}

// fakeParents are lists by id; deleted ones are not there.
type fakeParents struct {
	docs    map[string]map[string]any
	queries int
}

func (f *fakeParents) Get(ref string, key any) (map[string]any, error) {
	id, _ := key.(string)
	return f.docs[id], nil
}

func (f *fakeParents) Keys(ref string, flt storage.Filter) ([]any, error) {
	f.queries++
	var out []any
	for id, d := range f.docs {
		ok, err := storage.Match(d, flt)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	return out, nil
}

// TestParentRulesAgree is the invariant: for every caller and document, the read rule applied
// as a filter and applied in memory give the same answer, orphans and negations included.
func TestParentRulesAgree(t *testing.T) {
	parents := &fakeParents{docs: map[string]map[string]any{
		"l1": {"id": "l1", "members": []any{"u1", "u2"}, "public": false, "owner": "u1", "name": "a"},
		"l2": {"id": "l2", "members": []any{"u3"}, "public": true, "owner": "u3", "name": "b"},
		"l3": {"id": "l3", "members": []any{}, "owner": nil, "name": nil}, // public missing
	}}
	var items []map[string]any
	for _, l := range []any{"l1", "l2", "l3", "gone", nil, ""} { // gone: deleted or never existed
		for _, owner := range []any{"u1", "u9"} {
			doc := map[string]any{"id": fmt.Sprint(len(items)), "_meta": map[string]any{"owner": owner}}
			if l != nil {
				doc["list_id"] = l
			}
			items = append(items, doc)
		}
	}
	users := []*User{nil, {ID: "u1"}, {ID: "u2"}, {ID: "u3"}, {ID: "u9"}, {ID: "u1", Roles: []string{"admin"}}}
	for _, src := range []string{
		"user != nil && user.id in parent(document.list_id).members",
		"parent(document.list_id).public == true || (user != nil && user.id in parent(document.list_id).members)",
		"user != nil && !(user.id in parent(document.list_id).members)",
		"user != nil && !(parent(document.list_id).public == true)",
		"user != nil && parent(document.list_id).owner != user.id",
		"user != nil && parent(document.list_id).owner == nil",
		"parent(document.list_id).public",
		"!parent(document.list_id).public",
		"user != nil && (parent(document.list_id).owner == user.id || document._meta.owner == user.id)",
		"hasRole(user, 'admin') || (user != nil && parent(document.list_id).name in ['a', 'b'])",
		"user != nil && 'u3' in parent(document.list_id).members && !(parent(document.list_id).name == 'b')",
	} {
		s, errs := parseParents(t, "read: "+quote(src))
		if len(errs) > 0 {
			t.Errorf("%s: %v", src, errs)
			continue
		}
		r := s.For(Read)
		for _, u := range users {
			v := Values{User: u, Now: time.Now(), Parents: parents}
			f, err := r.Filter(v)
			if err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			for _, d := range items {
				v.Document = d
				want, err := r.Allow(v)
				if err != nil {
					t.Fatalf("%s: allow: %v", src, err)
				}
				got, err := storage.Match(d, f)
				if err != nil {
					t.Fatalf("%s: match: %v", src, err)
				}
				if got != want {
					t.Errorf("%s as %v on %v: filter %v, rule %v", src, u, d, got, want)
				}
			}
		}
	}
}

func quote(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\"" }

func TestParentWriteRules(t *testing.T) {
	parents := &fakeParents{docs: map[string]map[string]any{
		"l1": {"id": "l1", "members": []any{"u1"}, "owner": "u1"},
		"l2": {"id": "l2", "members": []any{"u2"}, "owner": "u2"},
	}}
	s, errs := parseParents(t, `
create: user != nil && user.id in parent(data.list_id).members
update: user != nil && user.id in parent(document.list_id).members && user.id in parent(data.list_id).members
`)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	u := &User{ID: "u1"}
	for _, c := range []struct {
		op       Op
		doc, dat map[string]any
		want     bool
	}{
		{Create, nil, map[string]any{"list_id": "l1"}, true},
		{Create, nil, map[string]any{"list_id": "l2"}, false},
		{Create, nil, map[string]any{"list_id": "nope"}, false},
		{Create, nil, map[string]any{}, false},
		{Update, map[string]any{"list_id": "l1"}, map[string]any{"list_id": "l1"}, true},
		{Update, map[string]any{"list_id": "l1"}, map[string]any{"list_id": "l2"}, false}, // can't move into a list you aren't in
		{Update, map[string]any{"list_id": "l2"}, map[string]any{"list_id": "l1"}, false},
	} {
		got, err := s.For(c.op).Allow(Values{User: u, Document: c.doc, Data: c.dat, Parents: parents})
		if err != nil || got != c.want {
			t.Errorf("%s %v -> %v: got %v (%v), want %v", c.op, c.doc, c.dat, got, err, c.want)
		}
	}
	// Without a way to reach the parents the rule can't be decided: a denial.
	if ok, err := s.For(Create).Allow(Values{User: u, Data: map[string]any{"list_id": "l1"}}); ok || err == nil {
		t.Errorf("no parents: %v, %v", ok, err)
	}
}

func TestParentLookupsAreBounded(t *testing.T) {
	parents := &fakeParents{docs: map[string]map[string]any{"l1": {"id": "l1", "owner": "u1"}}}
	s, errs := parseParents(t, "read: user != nil && parent(document.list_id).owner == user.id && parent(document.list_id).public != true")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !s.UsesParents() || len(s.For(Read).ParentUses()) != 2 {
		t.Fatalf("uses: %+v", s.For(Read).ParentUses())
	}
	// A caller the rule already refuses (user == nil) costs no lookup.
	if f, err := s.For(Read).Filter(Values{Parents: parents}); err != nil || f != storage.Filter(storage.Const(false)) || parents.queries != 0 {
		t.Fatalf("anonymous: %v %v, %d queries", f, err, parents.queries)
	}
}
