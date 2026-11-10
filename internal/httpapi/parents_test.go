package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// parentFixture is lists (with members) and items (list_id → lists) whose rules follow the
// reference: the example of design/parent-rules.md.
type parentFixture struct {
	*fixture
	key              string
	ada, bob         string // sessions
	adaID, bobID     string
	adaList, bobList string
}

func newParentFixture(t *testing.T) *parentFixture {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"team/realm.yaml":                "signup: open\n",
		"team/app/lists/schema.json":     `{"type":"object","properties":{"name":{"type":"string"},"members":{"type":"array","items":{"type":"string"}}},"required":["name"]}`,
		"team/app/lists/collection.yaml": "rules:\n  read: user != nil && user.id in document.members\n  create: user != nil && data.members == [user.id]\n  update: user != nil && user.id in document.members\n  delete: user != nil && user.id in document.members\n",
		"team/app/items/schema.json":     `{"type":"object","properties":{"title":{"type":"string"},"list_id":{"type":"string"}},"required":["title","list_id"]}`,
		"team/app/items/collection.yaml": `references:
  list: {field: list_id, collection: lists}
rules:
  read: user != nil && user.id in parent(document.list_id).members
  create: user != nil && user.id in parent(data.list_id).members
  update: user != nil && user.id in parent(document.list_id).members && user.id in parent(data.list_id).members
  delete: user != nil && user.id in parent(document.list_id).members
`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{Store: authtest.NewMemStore(), Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}), Settings: reg.Realms["team"].Settings}
	f := &parentFixture{fixture: newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(string) *auth.Users { return svc }
	})}
	ctx := context.Background()
	_, f.key, err = svc.CreateAPIKey(ctx, "seed", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	signup := func(email string) (string, string) {
		p, token, err := svc.Signup(ctx, email, "dev-p4ssw0rd!", "")
		if err != nil {
			t.Fatal(err)
		}
		return token, p.User.ID
	}
	f.ada, f.adaID = signup("ada@example.com")
	f.bob, f.bobID = signup("bob@example.com")
	f.adaList = f.seed(t, "/v1/team/app/lists", fmt.Sprintf(`{"name":"ada's","members":[%q]}`, f.adaID))
	f.bobList = f.seed(t, "/v1/team/app/lists", fmt.Sprintf(`{"name":"bob's","members":[%q]}`, f.bobID))
	return f
}

func (f *parentFixture) seed(t *testing.T, path, body string) string {
	t.Helper()
	rec, out := f.doH(t, "POST", path, body, bearer(f.key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed %s: %d %v", path, rec.Code, out)
	}
	return out["id"].(string)
}

func (f *parentFixture) titles(t *testing.T, session string) []string {
	t.Helper()
	rec, out := f.doH(t, "GET", "/v1/team/app/items", "", bearer(session))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	var titles []string
	for _, it := range out["items"].([]any) {
		titles = append(titles, it.(map[string]any)["title"].(string))
	}
	slices.Sort(titles)
	return titles
}

func TestParentRulesOverHTTP(t *testing.T) {
	f := newParentFixture(t)
	a1 := f.seed(t, "/v1/team/app/items", fmt.Sprintf(`{"title":"milk","list_id":%q}`, f.adaList))
	f.seed(t, "/v1/team/app/items", fmt.Sprintf(`{"title":"eggs","list_id":%q}`, f.adaList))
	b1 := f.seed(t, "/v1/team/app/items", fmt.Sprintf(`{"title":"beer","list_id":%q}`, f.bobList))
	f.seed(t, "/v1/team/app/items", `{"title":"orphan","list_id":"gone"}`)

	t.Run("a list shows what the caller's lists hold", func(t *testing.T) {
		if got := f.titles(t, f.ada); !slices.Equal(got, []string{"eggs", "milk"}) {
			t.Errorf("ada sees %v", got)
		}
		if got := f.titles(t, f.bob); !slices.Equal(got, []string{"beer"}) {
			t.Errorf("bob sees %v", got)
		}
		if rec, _ := f.doH(t, "GET", "/v1/team/app/items", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous: %d", rec.Code)
		}
	})

	t.Run("a fetch agrees with the list", func(t *testing.T) {
		if rec, _ := f.doH(t, "GET", "/v1/team/app/items/"+a1, "", bearer(f.ada)); rec.Code != http.StatusOK {
			t.Errorf("own item: %d", rec.Code)
		}
		if rec, _ := f.doH(t, "GET", "/v1/team/app/items/"+b1, "", bearer(f.ada)); rec.Code != http.StatusNotFound {
			t.Errorf("another list's item: %d", rec.Code)
		}
	})

	t.Run("create needs the list", func(t *testing.T) {
		for _, c := range []struct {
			list string
			want int
		}{{f.adaList, http.StatusCreated}, {f.bobList, http.StatusForbidden}, {"gone", http.StatusForbidden}} {
			rec, out := f.doH(t, "POST", "/v1/team/app/items", fmt.Sprintf(`{"title":"new","list_id":%q}`, c.list), bearer(f.ada))
			if rec.Code != c.want {
				t.Errorf("create in %s: %d %v, want %d", c.list, rec.Code, out, c.want)
			}
		}
	})

	t.Run("an item can't be moved to a list the caller isn't in", func(t *testing.T) {
		rec, out := f.doH(t, "PUT", "/v1/team/app/items/"+a1, fmt.Sprintf(`{"title":"milk","list_id":%q}`, f.bobList), bearer(f.ada))
		if rec.Code != http.StatusForbidden {
			t.Errorf("move: %d %v", rec.Code, out)
		}
		if rec, out := f.doH(t, "PUT", "/v1/team/app/items/"+a1, fmt.Sprintf(`{"title":"oat milk","list_id":%q}`, f.adaList), bearer(f.ada)); rec.Code != http.StatusOK {
			t.Errorf("edit: %d %v", rec.Code, out)
		}
	})

	t.Run("delete needs a readable item", func(t *testing.T) {
		if rec, _ := f.doH(t, "DELETE", "/v1/team/app/items/"+b1, "", bearer(f.ada)); rec.Code != http.StatusNotFound {
			t.Errorf("another list's item: %d", rec.Code)
		}
		if rec, _ := f.doH(t, "DELETE", "/v1/team/app/items/"+b1, "", bearer(f.bob)); rec.Code != http.StatusNoContent {
			t.Errorf("own item: %d", rec.Code)
		}
	})

	t.Run("a membership change moves the access without touching the items", func(t *testing.T) {
		before := len(f.titles(t, f.bob))
		rec, out := f.doH(t, "PATCH", "/v1/team/app/lists/"+f.adaList, fmt.Sprintf(`{"members":[%q,%q]}`, f.adaID, f.bobID), bearer(f.key))
		if rec.Code != http.StatusOK {
			t.Fatalf("add bob: %d %v", rec.Code, out)
		}
		if got := len(f.titles(t, f.bob)); got <= before {
			t.Errorf("bob sees %d items after joining, %d before", got, before)
		}
		f.doH(t, "PATCH", "/v1/team/app/lists/"+f.adaList, fmt.Sprintf(`{"members":[%q]}`, f.adaID), bearer(f.key))
		if got := f.titles(t, f.bob); len(got) != before {
			t.Errorf("bob sees %v after leaving", got)
		}
	})

	t.Run("deleting the list hides its items", func(t *testing.T) {
		if rec, _ := f.doH(t, "DELETE", "/v1/team/app/lists/"+f.adaList, "", bearer(f.key)); rec.Code != http.StatusNoContent {
			t.Fatalf("delete list: %d", rec.Code)
		}
		// With no list left that qualifies the rule is false for ada: the same answer as any
		// rule that can't be true.
		rec, _ := f.doH(t, "GET", "/v1/team/app/items", "", bearer(f.ada))
		if rec.Code != http.StatusForbidden {
			t.Errorf("ada after her list is gone: %d", rec.Code)
		}
		if rec, _ := f.doH(t, "GET", "/v1/team/app/items/"+a1, "", bearer(f.ada)); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("fetch after her list is gone: %d", rec.Code)
		}
	})
}

func TestParentRulesInABatch(t *testing.T) {
	f := newParentFixture(t)
	op := func(list string) string {
		return fmt.Sprintf(`{"op":"create","collection":"items","document":{"title":"x","list_id":%q}}`, list)
	}
	// One operation is outside the caller's lists: the whole batch is refused.
	rec, out := f.doH(t, "POST", "/v1/team/app/_batch", `{"operations":[`+op(f.adaList)+`,`+op(f.bobList)+`]}`, bearer(f.ada))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a batch writing to a list the caller isn't in: %d %v", rec.Code, out)
	}
	if got := f.titles(t, f.ada); len(got) != 0 {
		t.Errorf("the batch wasn't atomic: %v", got)
	}
	if rec, out := f.doH(t, "POST", "/v1/team/app/_batch", `{"operations":[`+op(f.adaList)+`]}`, bearer(f.ada)); rec.Code != http.StatusOK {
		t.Fatalf("a batch inside her list: %d %v", rec.Code, out)
	}
	if got := f.titles(t, f.ada); len(got) != 1 {
		t.Errorf("ada sees %v", got)
	}
}
