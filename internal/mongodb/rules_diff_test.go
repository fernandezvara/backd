package mongodb

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Guards against the worst bug class in access rules: a read rule whose
// pushed-down MongoDB filter disagrees with the rule, which would list
// documents the rule forbids (or hide ones it allows). Risk R13.
//
// The reference is the in-memory evaluation of the rule (expr), per
// document and caller. Lists and single reads (storage.Fetch, what GET
// /{id}, PUT, PATCH and DELETE use) must both return exactly the allowed
// documents. The one case expr can't decide is an ordering comparison with
// a missing or null field, or its use as a boolean (nil > 3 and !nil are
// errors); the filter's semantics for those are pinned down by
// TestReadRuleFiltersOnMissingFields instead.

const diffSchema = `{
  "type": "object",
  "properties": {
    "status":    {"type": "string"},
    "n":         {"type": "integer"},
    "score":     {"type": "number"},
    "published": {"type": "boolean"},
    "tags":      {"type": "array", "items": {"type": "string"}},
    "members":   {"type": "array", "items": {"type": "string"}},
    "address":   {"type": "object", "properties": {"city": {"type": "string"}}},
    "opt":       {"type": ["string", "null"]}
  }
}`

// diffRules are read rules exercising every construct the translator supports.
var diffRules = []string{
	`"true"`,
	`user != nil`,
	`user == nil`,
	`document.published == true || (user != nil && document._meta.owner == user.id)`,
	`"document.status in ['a', 'b']"`,
	`"!(document.status == 'a')"`,
	`"document.status != 'a'"`,
	`document.n > 3 && document.n <= 10`,
	`"3 < document.n"`,
	`"document.n >= 10 || document.n < 1"`,
	`document.score >= 1.5`,
	`"document.score < 2 && !(document.score == 1)"`,
	`"user != nil && user.id in document.members"`,
	`"'x' in document.tags"`,
	`document.published`,
	`not document.published`,
	`"hasRole(user, 'admin') || document.status == 'public'"`,
	`"document.address.city == 'Madrid'"`,
	`"user != nil && document.address.city == user.email"`,
	`document._meta.created_at > now - duration('1h')`,
	`"document.id != 'nope' && document._meta.version >= 1"`,
	`"(document.status == 'a' || document.status == 'b') && !(document.n == 1) || user == nil && document.published"`,
	`"user != nil && document.status in user.roles"`,
	// Guards and short-circuiting: the right side must not be evaluated
	// (or translated) for callers the left side already decides.
	`user == nil || document._meta.owner == user.id`,
	`"user != nil && (document._meta.owner == user.id || user.id in document.members)"`,
	`"hasRole(user, 'editor') && !(document.status == 'a') || user != nil && user.email == document.address.city"`,
	`"!(user != nil && hasRole(user, 'admin')) && document.published"`,
	// Ownerless documents and timestamps.
	`document._meta.owner == nil`,
	`"document._meta.owner != nil && document._meta.updated_at < now - duration('30m')"`,
	`document._meta.created_at <= now && document._meta.version == 1`,
	// Null and missing fields, where expr can decide.
	`"document.opt == 's'"`,
	`document.opt != nil`,
	`"document.status == nil || document.status == 'a'"`,
}

// diffUsers are the callers: anonymous, users with and without roles.
// Documents are owned by u1..u4 or nobody, and list some of them as members.
var diffUsers = map[string]*rules.User{
	"anonymous":   nil,
	"u1 (a)":      {ID: "u1", Email: "Madrid", Roles: []string{"a"}},
	"u2 (admin)":  {ID: "u2", Email: "Oslo", Roles: []string{"admin"}},
	"u3":          {ID: "u3", Email: "ada@example.com"},
	"u4 (editor)": {ID: "u4", Email: "bob@example.com", Roles: []string{"editor"}},
}

var diffRoles = []string{"admin", "a", "editor", "staff"}

func diffFixture(t *testing.T) (*registry.Collection, *Repository) {
	t.Helper()
	return provisionOne(t, diffSchema)
}

// provisionOne provisions a collection with the schema in a realm that
// declares diffRoles.
func provisionOne(t *testing.T, schema string) (*registry.Collection, *Repository) {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	reg := loadRegistryWith(t, realm, "roles:\n  admin: {}\n  a: {}\n  editor: {}\n  staff: {}\n", map[string]string{"app/items": schema})
	log, _ := testLogger()
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "items")
	return c, &Repository{coll: client.Database(c.MongoDatabase).Collection(c.Name)}
}

func compileRead(t *testing.T, c *registry.Collection, src string) *rules.Rule {
	t.Helper()
	return compileRules(t, c, "read: "+src+"\n").For(rules.Read)
}

// compileRules compiles a rules.yaml as startup does, for the collection.
func compileRules(t *testing.T, c *registry.Collection, content string) *rules.Set {
	t.Helper()
	set, errs := rules.Parse([]byte(content), rules.Schema{
		DocumentField: c.IsKnownField,
		DataField:     func(p string) bool { _, ok := c.Fields[p]; return ok },
		ScalarField:   c.ScalarField,
		ArrayField:    c.ArrayField,
		Roles:         diffRoles,
	})
	if len(errs) > 0 {
		t.Fatalf("%s: %v", content, errs)
	}
	return set
}

// all reads every document, or only those the filter allows.
func all(t *testing.T, repo *Repository, access storage.Filter) map[string]storage.Document {
	t.Helper()
	out := map[string]storage.Document{}
	for skip := 0; ; skip += 100 {
		page, err := repo.List(context.Background(), storage.Query{Access: access, Limit: 100, Skip: skip})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page.Items {
			out[d["id"].(string)] = d
		}
		if !page.HasMore {
			return out
		}
	}
}

// listed returns the ids MongoDB lists for the rule and caller.
func listed(t *testing.T, repo *Repository, r *rules.Rule, v rules.Values) []string {
	t.Helper()
	f, err := r.Filter(v)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	return listedBy(t, repo, f)
}

// listedBy returns the ids MongoDB lists with the access filter.
func listedBy(t *testing.T, repo *Repository, f storage.Filter) []string {
	t.Helper()
	var ids []string
	switch f {
	case storage.Const(false):
	case storage.Const(true):
		for id := range all(t, repo, nil) {
			ids = append(ids, id)
		}
	default:
		for id := range all(t, repo, f) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func insert(t *testing.T, repo *Repository, fields map[string]any, created time.Time, owner any) string {
	t.Helper()
	id := fmt.Sprintf("d%05d", len(fields)+rand.IntN(1e9))
	doc := map[string]any{"id": id, "_meta": map[string]any{
		"created_at": created, "updated_at": created, "version": int64(1),
		"owner": owner, "created_by": "key:test", "updated_by": "key:test",
	}}
	for k, v := range fields {
		doc[k] = v
	}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatalf("insert %v: %v", doc, err)
	}
	return id
}

// Part A: generated documents, some with missing or null fields, owned by
// the test users or by nobody.
func TestReadRuleFiltersMatchRules(t *testing.T) {
	c, repo := diffFixture(t)
	rng := rand.New(rand.NewPCG(7, 13))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for range 300 {
		fields := map[string]any{
			"status":    pick("a", "b", "c", "public", "admin", ""),
			"n":         pick(int64(0), int64(1), int64(3), int64(4), int64(5), int64(10), int64(11)),
			"score":     pick(0.5, 1.0, 1.5, 1.9999, 2.0, 2.25),
			"published": pick(true, false),
			"tags":      pick([]any{}, []any{"x"}, []any{"x", "y"}, []any{"y"}),
			"members":   pick([]any{}, []any{"u1"}, []any{"u2", "u1"}, []any{"u3"}, []any{"u4", "u3"}),
			"address":   map[string]any{"city": pick("Madrid", "Oslo", "ada@example.com", "")},
			"opt":       pick("s", nil),
		}
		// One document in five lacks a field, as documents written before
		// the field existed do.
		if rng.IntN(5) == 0 {
			delete(fields, pick("status", "n", "score", "published", "tags", "members", "address", "opt").(string))
		}
		created := now.Add(-time.Duration(rng.IntN(120)) * time.Minute)
		insert(t, repo, fields, created, pick("u1", "u2", "u3", "u4", nil))
	}
	docs := all(t, repo, nil)
	var undecided int
	for _, src := range diffRules {
		mismatches, u := compare(t, repo, compileRead(t, c, src), nil, src, docs, now, rng)
		for _, m := range mismatches {
			t.Error(m)
		}
		undecided += u
	}
	t.Logf("%d rules × %d callers × %d documents compared; %d cases left to TestReadRuleFiltersOnMissingFields (nil operands expr can't evaluate)",
		len(diffRules), len(diffUsers), len(docs), undecided)
}

// TestSampleReadRules runs the comparison for the read rule of every
// sample rules.yaml in the repository, on documents generated from its
// schema.
func TestSampleReadRules(t *testing.T) {
	testClient(t) // skips without MongoDB, before the subtests would
	var n int
	for _, s := range samples(t) {
		if s.rules == "" {
			continue
		}
		t.Run(s.path, func(t *testing.T) {
			c, repo := provisionOne(t, s.schema)
			r := compileRules(t, c, s.rules).For(rules.Read)
			if r == nil {
				t.Skip("no read rule")
			}
			n++
			rng := rand.New(rand.NewPCG(uint64(len(s.path)), 29))
			gen := docGen{rng: rng}
			schema := parseSchema(t, s.schema)
			now := time.Now().UTC().Truncate(time.Millisecond)
			for inserted, tries := 0, 0; inserted < 250; tries++ {
				if tries == 20000 {
					t.Fatalf("only %d of 20000 generated documents are valid; the generator doesn't fit this schema", inserted)
				}
				body := gen.object(schema, 0)
				v := decodeJSON(t, body)
				if c.Schema.Validate(v) != nil {
					continue // only documents the API would store
				}
				fields := jsonnum.Normalize(v).(map[string]any)
				owner := []any{"u1", "u2", "u3", "u4", nil}[rng.IntN(5)]
				insert(t, repo, fields, now.Add(-time.Duration(rng.IntN(120))*time.Minute), owner)
				inserted++
			}
			mismatches, _ := compare(t, repo, r, nil, s.path, all(t, repo, nil), now, rng)
			for _, m := range mismatches {
				t.Error(m)
			}
		})
	}
	if n < 4 {
		t.Errorf("only %d sample read rules compared", n)
	}
}

// compare checks, for every test caller, that listing with the rule's
// filter returns exactly the documents the rule allows in memory, and that
// single reads of documents agree. It returns the disagreements, and how
// many cases expr couldn't decide. break_, if not nil, alters the filters
// to check that compare notices (TestReadRuleCheckCatchesBrokenFilters).
func compare(t *testing.T, repo *Repository, r *rules.Rule, break_ func(storage.Filter) storage.Filter, src string, docs map[string]storage.Document, now time.Time, rng *rand.Rand) (mismatches []string, undecided int) {
	t.Helper()
	filter := func(v rules.Values) storage.Filter {
		f, err := r.Filter(v)
		if err != nil {
			t.Fatalf("%s: filter: %v", src, err)
		}
		if break_ != nil {
			f = storage.Simplify(break_(f))
		}
		return f
	}
	for name, u := range diffUsers {
		v := rules.Values{User: u, Now: now}
		allowed := map[string]bool{}
		skip := map[string]bool{}
		for id, d := range docs {
			dv := v
			dv.Document = d
			ok, err := r.Allow(dv)
			switch {
			case err != nil && (strings.Contains(err.Error(), "<nil>") || strings.Contains(err.Error(), "is nil")):
				skip[id] = true // a missing or null operand expr can't use: see Part B
				undecided++
			case err != nil:
				t.Fatalf("%s as %s: evaluating on %v: %v", src, name, d, err)
			case ok:
				allowed[id] = true
			}
		}
		var want, got []string
		for id := range allowed {
			want = append(want, id)
		}
		f := filter(v)
		var ids0 []string
		switch f {
		case storage.Const(false):
		case storage.Const(true):
			ids0 = listedBy(t, repo, nil)
		default:
			ids0 = listedBy(t, repo, f)
		}
		for _, id := range ids0 {
			if !skip[id] {
				got = append(got, id)
			}
		}
		// storage.Match, which `backd rules test` evaluates read rules with, must
		// list exactly what MongoDB lists, including the cases expr can't decide.
		var inMemory []string
		for id, d := range docs {
			ok, err := storage.Match(d, f)
			if err != nil {
				t.Fatalf("%s as %s: storage.Match: %v", src, name, err)
			}
			if ok {
				inMemory = append(inMemory, id)
			}
		}
		slices.Sort(inMemory)
		sortedMongo := slices.Clone(ids0)
		slices.Sort(sortedMongo)
		if !slices.Equal(inMemory, sortedMongo) {
			mismatches = append(mismatches, fmt.Sprintf("storage.Match %s as %s: in memory %d documents, MongoDB lists %d\n  only in memory: %v\n  only in MongoDB: %v",
				src, name, len(inMemory), len(sortedMongo), diff(inMemory, sortedMongo), diff(sortedMongo, inMemory)))
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			mismatches = append(mismatches, fmt.Sprintf("read %s as %s: filter lists %d documents, rule allows %d\n  only listed: %v\n  only allowed: %v",
				src, name, len(got), len(want), diff(got, want), diff(want, got)))
		}

		// Single reads, on a sample of documents.
		ids := make([]string, 0, len(docs))
		for id := range docs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for range 30 {
			id := ids[rng.IntN(len(ids))]
			if skip[id] {
				continue
			}
			found := false
			switch f {
			case storage.Const(false): // the API answers 401/403 without reading
			case storage.Const(true):
				found = true
			default:
				_, err := storage.Fetch(context.Background(), repo, id, f)
				if err != nil && !errors.Is(err, storage.ErrNotFound) {
					t.Fatal(err)
				}
				found = err == nil
			}
			if found != allowed[id] {
				mismatches = append(mismatches, fmt.Sprintf("read %s as %s: single read of %s returns it: %v, rule allows it: %v", src, name, id, found, allowed[id]))
			}
		}
	}
	return mismatches, undecided
}

// TestReadRuleCheckCatchesBrokenFilters proves the comparison above would
// catch a broken translation: each mutation below alters the filters the
// way a translator bug would, and must make some rule disagree.
func TestReadRuleCheckCatchesBrokenFilters(t *testing.T) {
	c, repo := diffFixture(t)
	rng := rand.New(rand.NewPCG(19, 23))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for range 120 {
		insert(t, repo, map[string]any{
			"status":    pick("a", "b", "public"),
			"n":         pick(int64(1), int64(3), int64(4), int64(10)),
			"score":     pick(1.0, 1.5, 2.0),
			"published": pick(true, false),
			"tags":      pick([]any{}, []any{"x"}),
			"members":   pick([]any{}, []any{"u1"}, []any{"u3"}),
			"address":   map[string]any{"city": pick("Madrid", "Oslo")},
			"opt":       pick("s", nil),
		}, now.Add(-time.Duration(rng.IntN(120))*time.Minute), pick("u1", "u2", nil))
	}
	docs := all(t, repo, nil)
	mutations := map[string]func(storage.Filter) storage.Filter{
		"negation lost": mutate(func(f storage.Filter) storage.Filter {
			if n, ok := f.(storage.Not); ok {
				return n.Filter
			}
			return f
		}),
		"&& and || swapped": mutate(func(f storage.Filter) storage.Filter {
			switch x := f.(type) {
			case storage.And:
				return storage.Or(x)
			case storage.Or:
				return storage.And(x)
			}
			return f
		}),
		"right side dropped": mutate(func(f storage.Filter) storage.Filter {
			if a, ok := f.(storage.And); ok {
				return a[0]
			}
			return f
		}),
		"!= translated as ==": mutateCond(func(c storage.Condition) storage.Condition {
			if c.Op == storage.OpNe {
				c.Op = storage.OpEq
			}
			return c
		}),
		"> translated as >=": mutateCond(func(c storage.Condition) storage.Condition {
			if c.Op == storage.OpGt {
				c.Op = storage.OpGte
			}
			return c
		}),
		"null check flipped": mutateCond(func(c storage.Condition) storage.Condition {
			if c.Op == storage.OpIsNull {
				c.Value = !c.Value.(bool)
			}
			return c
		}),
	}
	for name, m := range mutations {
		caught := 0
		for _, src := range diffRules {
			mismatches, _ := compare(t, repo, compileRead(t, c, src), m, src, docs, now, rng)
			if len(mismatches) > 0 {
				caught++
			}
		}
		if caught == 0 {
			t.Errorf("mutation %q went unnoticed by every rule", name)
		} else {
			t.Logf("mutation %q caught by %d of %d rules", name, caught, len(diffRules))
		}
	}
}

// mutate applies fn to every node of a filter tree, bottom up.
func mutate(fn func(storage.Filter) storage.Filter) func(storage.Filter) storage.Filter {
	var walk func(storage.Filter) storage.Filter
	walk = func(f storage.Filter) storage.Filter {
		switch x := f.(type) {
		case storage.And:
			out := make(storage.And, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return fn(out)
		case storage.Or:
			out := make(storage.Or, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return fn(out)
		case storage.Not:
			return fn(storage.Not{Filter: walk(x.Filter)})
		}
		return fn(f)
	}
	return walk
}

func mutateCond(fn func(storage.Condition) storage.Condition) func(storage.Filter) storage.Filter {
	return mutate(func(f storage.Filter) storage.Filter {
		if c, ok := f.(storage.Condition); ok {
			return fn(c)
		}
		return f
	})
}

// Part B: missing and null fields. The rule language errors on comparisons
// with nil, so the filter's semantics are the contract here (documented in
// the access rules page): a missing or null field never equals, exceeds or
// is in anything; == nil matches it; != and ! match it.
func TestReadRuleFiltersOnMissingFields(t *testing.T) {
	c, repo := diffFixture(t)
	now := time.Now().UTC()
	ids := map[string]string{
		"missing": insert(t, repo, map[string]any{}, now, nil),
		"null":    insert(t, repo, map[string]any{"opt": nil}, now, nil),
		"s":       insert(t, repo, map[string]any{"opt": "s", "status": "a", "n": int64(5), "published": true, "tags": []any{"x"}}, now, nil),
		"t":       insert(t, repo, map[string]any{"opt": "t", "status": "b", "n": int64(1), "published": false, "tags": []any{}}, now, nil),
	}
	for _, tt := range []struct {
		rule string
		want []string
	}{
		{`"document.opt == 's'"`, []string{"s"}},
		{`"document.opt != 's'"`, []string{"missing", "null", "t"}},
		{`"!(document.opt == 's')"`, []string{"missing", "null", "t"}},
		{`document.opt == nil`, []string{"missing", "null"}},
		{`document.opt != nil`, []string{"s", "t"}},
		{`"document.opt in ['s', 't']"`, []string{"s", "t"}},
		{`"document.opt > 'a'"`, []string{"s", "t"}},
		{`document.n > 3`, []string{"s"}},
		{`"!(document.n > 3)"`, []string{"missing", "null", "t"}},
		{`document.published`, []string{"s"}},
		{`not document.published`, []string{"missing", "null", "t"}},
		{`"'x' in document.tags"`, []string{"s"}},
		{`"!('x' in document.tags)"`, []string{"missing", "null", "t"}},
		{`"document.n > 3 || document.status == 'b'"`, []string{"s", "t"}},
	} {
		var want []string
		for _, k := range tt.want {
			want = append(want, ids[k])
		}
		slices.Sort(want)
		if got := listed(t, repo, compileRead(t, c, tt.rule), rules.Values{Now: now}); !slices.Equal(got, want) {
			t.Errorf("read %s: lists %v, want %v", tt.rule, names(ids, got), tt.want)
		}
	}
}

func diff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func names(ids map[string]string, got []string) string {
	var out []string
	for _, g := range got {
		for k, v := range ids {
			if v == g {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}
