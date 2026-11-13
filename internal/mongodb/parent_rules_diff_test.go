package mongodb

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// repoParents is rules.Parents over a real collection: what the HTTP layer does.
type repoParents struct {
	repo *Repository
	key  string
}

func (p repoParents) Get(_ string, key any) (map[string]any, error) {
	page, err := p.repo.List(context.Background(), storage.Query{Filter: storage.Condition{Field: p.key, Op: storage.OpEq, Value: key}, Limit: 1})
	if err != nil || len(page.Items) == 0 {
		return nil, err
	}
	return page.Items[0], nil
}

func (p repoParents) Keys(_ string, f storage.Filter) ([]any, error) {
	page, err := p.repo.List(context.Background(), storage.Query{Filter: f, Limit: 1000})
	if err != nil {
		return nil, err
	}
	var out []any
	for _, d := range page.Items {
		if v, ok := docutil.GetPath(d, p.key); ok && v != nil {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	return out, nil
}

// TestParentRulesMatchMongo (R13 for parent()): for every caller and document, listing with the
// semi-join filter MongoDB runs returns what the rule allows when evaluated on each document in
// memory, with parents that are missing, empty, public and owned by other people.
func TestParentRulesMatchMongo(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	files := map[string]string{
		"app/lists":                 `{"type":"object","properties":{"members":{"type":"array","items":{"type":"string"}},"public":{"type":"boolean"},"owner":{"type":["string","null"]},"name":{"type":"string"}}}`,
		"app/items":                 `{"type":"object","properties":{"list_id":{"type":["string","null"]},"title":{"type":"string"}}}`,
		"app/items/collection.yaml": "references:\n  list: {field: list_id, collection: lists}\nrules:\n  read: user != nil && user.id in parent(document.list_id).members\n",
		"app/items/indexes.json":    `[{"fields": ["list_id"]}]`,
		"app/lists/indexes.json":    `[{"fields": ["members"]}]`,
		"app/lists/collection.yaml": "rules:\n  read: 'true'\n",
	}
	reg := loadRegistryWith(t, realm, "auth:\n  strategies:\n    password: [password]\nroles:\n  admin: {}\n  a: {}\n  editor: {}\n", files)
	log, _ := testLogger()
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := reg.Collection(realm, "app", "items")
	lists, _ := reg.Collection(realm, "app", "lists")
	itemRepo := &Repository{coll: client.Database(items.MongoDatabase).Collection(items.Name)}
	listRepo := &Repository{coll: client.Database(lists.MongoDatabase).Collection(lists.Name)}

	rng := rand.New(rand.NewPCG(11, 17))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"l1", "l2", "l3", "l4", "l5"} {
		fields := map[string]any{"id": id, "name": pick("a", "b", "c")}
		fields["members"] = pick([]any{}, []any{"u1"}, []any{"u1", "u3"}, []any{"u2"}, []any{"u3", "u4"})
		if rng.IntN(3) > 0 {
			fields["public"] = pick(true, false)
		}
		if rng.IntN(2) == 0 {
			fields["owner"] = pick("u1", "u3", nil)
		}
		doc := map[string]any{"_meta": map[string]any{"created_at": now, "updated_at": now, "version": int64(1), "owner": nil, "created_by": "key:test", "updated_by": "key:test"}}
		for k, v := range fields {
			doc[k] = v
		}
		if err := listRepo.Create(context.Background(), doc); err != nil {
			t.Fatal(err)
		}
	}
	for range 200 {
		f := map[string]any{"title": "x"}
		switch rng.IntN(8) {
		case 0: // no reference at all
		case 1:
			f["list_id"] = nil
		case 2:
			f["list_id"] = "gone"
		default:
			f["list_id"] = pick("l1", "l2", "l3", "l4", "l5")
		}
		insert(t, itemRepo, f, now, pick("u1", "u2", nil))
	}

	parents := repoParents{repo: listRepo, key: "id"}
	for _, src := range []string{
		"user != nil && user.id in parent(document.list_id).members",
		"parent(document.list_id).public == true || (user != nil && user.id in parent(document.list_id).members)",
		"user != nil && !(user.id in parent(document.list_id).members)",
		"user != nil && !(parent(document.list_id).public == true)",
		"user != nil && parent(document.list_id).owner != user.id",
		"user != nil && (parent(document.list_id).owner == user.id || document._meta.owner == user.id)",
		"parent(document.list_id).name in ['a', 'b'] && !parent(document.list_id).public",
	} {
		set := compileParentRules(t, items, lists, "read: "+quoteYAML(src))
		callers := map[string]rules.Values{}
		for name, u := range diffUsers {
			callers[name] = rules.Values{User: u, Now: now, Parents: parents}
		}
		mismatches, _ := compareCallers(t, itemRepo, set.For(rules.Read), nil, src, all(t, itemRepo, nil), callers, rng)
		for _, m := range mismatches {
			t.Error(m)
		}
	}
}

func quoteYAML(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\"" }

// compileParentRules parses rules of items that follow its reference "list" to lists, as the
// registry does: with the reference known while parsing, and bound to the target afterwards.
func compileParentRules(t *testing.T, items, lists *registry.Collection, content string) *rules.Set {
	t.Helper()
	var doc map[string]string
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	schema := func(c *registry.Collection) rules.Schema {
		return rules.Schema{
			DocumentField: c.IsKnownField, DataField: func(p string) bool { _, ok := c.Fields[p]; return ok },
			ScalarField: c.ScalarField, ArrayField: c.ArrayField, EncryptedField: c.IsEncrypted, SearchableField: c.IsSearchable,
			Roles: diffRoles,
		}
	}
	child := schema(items)
	child.ParentRef = func(p string) (rules.ParentRef, bool) { return rules.ParentRef{Name: "list"}, p == "list_id" }
	set, errs := rules.Parse(doc, child)
	if len(errs) == 0 {
		errs = set.BindParents(func(string) (rules.ParentTarget, bool) { return rules.ParentTarget{Schema: schema(lists)}, true })
	}
	if len(errs) > 0 {
		t.Fatalf("%s: %v", content, errs)
	}
	return set
}
