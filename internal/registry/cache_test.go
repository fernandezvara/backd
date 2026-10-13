package registry

import (
	"strings"
	"testing"
)

func TestCachePolicy(t *testing.T) {
	load := func(collection string) (*Collection, error) {
		reg, err := Load(writeTree(t, map[string]string{
			"shop/realm.yaml":               "roles:\n  admin: {}\n",
			"shop/db/items/schema.json":     personSchema,
			"shop/db/items/collection.yaml": collection,
		}))
		if err != nil {
			return nil, err
		}
		c, _ := reg.Collection("shop", "db", "items")
		return c, nil
	}
	c, err := load("cache: {public: true, max_age: 60}\nrules:\n  read: 'true'\n")
	if err != nil || c.Cache == nil || !c.Cache.Public || c.Cache.MaxAge != 60 {
		t.Fatalf("public cache = %+v %v", c, err)
	}
	// A rule that depends on the document can still allow anonymous callers.
	if c, err = load("cache: {public: true, max_age: 3600}\nrules:\n  read: document.name == 'x' || user != nil\n"); err != nil || c.Cache == nil {
		t.Errorf("document-dependent rule: %v", err)
	}
	if c, err = load("rules:\n  read: 'true'\n"); err != nil || c.Cache != nil {
		t.Errorf("no cache section: %+v %v", c, err)
	}
	if c, err = load("cache: {public: false}\nrules:\n  read: user != nil\n"); err != nil || c.Cache != nil {
		t.Errorf("public false: %+v %v", c, err)
	}

	for name, tc := range map[string]struct{ yaml, want string }{
		"no max_age":      {"cache: {public: true}\nrules:\n  read: 'true'\n", "max_age"},
		"too long":        {"cache: {public: true, max_age: 3601}\nrules:\n  read: 'true'\n", "between 1 and 3600"},
		"max_age alone":   {"cache: {max_age: 60}\nrules:\n  read: 'true'\n", "only applies"},
		"no read rule":    {"cache: {public: true, max_age: 60}\n", "has none"},
		"signed in only":  {"cache: {public: true, max_age: 60}\nrules:\n  read: user != nil\n", "never does"},
		"role only":       {"cache: {public: true, max_age: 60}\nrules:\n  read: hasRole(user, 'admin')\n", "never does"},
		"depends on time": {"cache: {public: true, max_age: 60}\nrules:\n  read: document._meta.created_at < now - duration('1h')\n", "`now`"},
		"unknown key":     {"cache: {public: true, max_age: 60, vary: x}\nrules:\n  read: 'true'\n", "vary"},
	} {
		if _, err := load(tc.yaml); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "collection.yaml") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
