package registry

import (
	"strings"
	"testing"
	"time"
)

func softTree(t *testing.T, realm, collection, rules string) (*Registry, error) {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":               realm,
		"shop/main/orders/schema.json":  ordersSchema,
		"shop/main/orders/indexes.json": `[{"fields": ["code"], "unique": true}, {"fields": ["total", "-code"]}, {"fields": ["buyer_name", "code"], "unique": true}]`,
	}
	if collection != "" {
		files["shop/main/orders/collection.yaml"] = collection
	}
	if rules != "" {
		files["shop/main/orders/rules.yaml"] = rules
	}
	return Load(writeTree(t, files))
}

func TestSoftDeleteSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml      string
		on        bool
		retention time.Duration
	}{
		"off by default":       {"", false, 0},
		"true":                 {"soft_delete: true\n", true, 0},
		"false":                {"soft_delete: false\n", false, 0},
		"an empty mapping":     {"soft_delete: {}\n", true, 0},
		"a retention":          {"soft_delete:\n  retention: 30d\n", true, 30 * 24 * time.Hour},
		"alongside the policy": {"soft_delete: true\non_owner_delete:\n  action: delete\n", true, 0},
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := softTree(t, "signup: open\n", tc.yaml, "")
			if err != nil {
				t.Fatal(err)
			}
			c, _ := reg.Collection("shop", "main", "orders")
			if (c.SoftDelete != nil) != tc.on || (c.SoftDelete != nil && c.SoftDelete.Retention != tc.retention) {
				t.Errorf("SoftDelete = %+v, want on=%v retention=%v", c.SoftDelete, tc.on, tc.retention)
			}
		})
	}
}

func TestSoftDeleteIndexes(t *testing.T) {
	reg, err := softTree(t, "signup: open\n", "soft_delete:\n  retention: 7d\n", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "orders")
	var got []string
	for _, ix := range c.Indexes {
		s := ix.String()
		if ix.Unique {
			s += " unique"
		}
		if ix.TTL {
			s += " ttl"
		}
		got = append(got, s)
	}
	// Unique indexes end with the deletion time; the others are untouched; the
	// retention adds the TTL index.
	want := "code,_meta.deleted_at unique | total,-code | buyer_name,code,_meta.deleted_at unique | _meta.purge_at ttl"
	if strings.Join(got, " | ") != want {
		t.Errorf("indexes:\n got %s\nwant %s", strings.Join(got, " | "), want)
	}
	// Without soft delete nothing changes.
	reg, _ = softTree(t, "signup: open\n", "", "")
	c, _ = reg.Collection("shop", "main", "orders")
	if c.Indexes[0].String() != "code" || c.Indexes[2].String() != "buyer_name,code" || len(c.Indexes) != 3 {
		t.Errorf("indexes without soft delete: %v", c.Indexes)
	}
}

func TestSoftDeleteErrors(t *testing.T) {
	for name, tc := range map[string]struct{ collection, rules, want string }{
		"retention too short": {"soft_delete:\n  retention: 10m\n", "", "retention: must be at least 1h0m0s"},
		"bad retention":       {"soft_delete:\n  retention: soon\n", "", "soft_delete.retention"},
		"unknown key":         {"soft_delete:\n  keep: 3\n", "", `unknown key "keep" in soft_delete`},
		"a number":            {"soft_delete: 3\n", "", "soft_delete must be true, false or a mapping"},
		"restore without it":  {"", "read: 'true'\nrestore: 'true'\n", "a restore rule only applies to a collection that soft-deletes"},
		"purge when off":      {"soft_delete: false\n", "read: 'true'\npurge: 'true'\n", "a purge rule only applies to a collection that soft-deletes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := softTree(t, "signup: open\nroles:\n  admin: {}\n", tc.collection, tc.rules)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// Soft delete needs no users: it works in a realm without auth.
	if _, err := softTree(t, "auth: disabled\n", "soft_delete: true\n", ""); err != nil {
		t.Errorf("soft_delete in a realm with auth disabled: %v", err)
	}
	// …but the policy for erased users still does.
	if _, err := softTree(t, "auth: disabled\n", "soft_delete: true\non_owner_delete:\n  action: delete\n", ""); err == nil || !strings.Contains(err.Error(), "only applies when the realm has auth enabled") {
		t.Errorf("on_owner_delete without auth: %v", err)
	}
	// Rules for restore and purge are fine when the collection soft-deletes.
	if _, err := softTree(t, "signup: open\nroles:\n  admin: {}\n", "soft_delete: true\n", "read: 'true'\nrestore: hasRole(user, 'admin')\npurge: hasRole(user, 'admin')\n"); err != nil {
		t.Errorf("restore and purge with soft_delete: %v", err)
	}
}
