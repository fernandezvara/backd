package mongodb

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

const encSchema = `{
  "type": "object",
  "properties": {
    "status": {"type": "string"},
    "n":      {"type": "integer"},
    "code":   {"type": "string"},
    "plain":  {"type": "string"},
    "secret": {"type": "string"}
  }
}`

const encConfig = "fields:\n  code:\n    encrypt: {key: K, search: equality}\n  secret:\n    encrypt: {key: K}\n"

// encryptedFixture is diffFixture for a collection with encrypted fields, with a ring that
// keeps its keys in memory.
func encryptedFixture(t *testing.T) (*registry.Collection, *Repository) {
	t.Helper()
	c, repo := provisionOneWith(t, encSchema, encConfig)
	key := bytes.Repeat([]byte{5}, 32)
	ring := &fieldcrypt.Ring{Realm: c.Realm, Store: fieldcrypt.NewMemStore(), Keys: func(context.Context, string) (fieldcrypt.Material, error) {
		return fieldcrypt.Material{Current: key, Version: 1}, nil
	}}
	if err := ring.Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	repo.c, repo.ring = c, ring
	return c, repo
}

// The pushdown property of TestReadRuleFiltersMatchRules, for a searchable encrypted field: a
// read rule that compares it with == or `in` lists exactly the documents the rule allows.
func TestReadRulesOnSearchableEncryptedFieldsMatchTheRules(t *testing.T) {
	c, repo := encryptedFixture(t)
	rng := rand.New(rand.NewPCG(11, 17))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for range 200 {
		fields := map[string]any{
			"status": pick("a", "b", "public"),
			"n":      pick(int64(1), int64(5), int64(10)),
			"code":   pick("a", "b", "Madrid", "Oslo", "ada@example.com", ""),
			"secret": "s",
		}
		if rng.IntN(5) == 0 {
			delete(fields, "code") // documents from before the field existed
		}
		insert(t, repo, fields, now.Add(-time.Duration(rng.IntN(120))*time.Minute), pick("u1", "u2", "u3", nil))
	}
	docs := all(t, repo, nil)
	for _, src := range []string{
		`"document.code == 'a'"`,
		`"document.code in ['a', 'b']"`,
		`"!(document.code == 'a')"`,
		`"!(document.code in ['a', 'Oslo'])"`,
		`"document.code == 'a' && document.n > 3"`,
		`"document.code == 'b' || document.status == 'public'"`,
		`"document.code == ''"`,
		`"user != nil && document.code == user.email"`,
		`"user != nil && document.code in [user.email, 'a']"`,
		`"document.code == 'a' && !(document.status == 'b') || user == nil"`,
		`"hasRole(user, 'admin') || document.code == 'Madrid'"`,
	} {
		mismatches, _ := compare(t, repo, compileRead(t, c, src), nil, src, docs, now, rng)
		for _, m := range mismatches {
			t.Error(m)
		}
	}
	// What a blind index can't answer is refused when the rules are compiled.
	for src, want := range map[string]string{
		`"document.code != 'a'"`:   "only answers == and `in`",
		`"document.code > 'a'"`:    "only answers == and `in`",
		`"document.secret == 'x'"`: "without `search: equality`",
		`"document.code"`:          "blind index",
	} {
		_, errs := rules.Parse(map[string]string{"read": strings.Trim(src, `"`)}, rules.Schema{
			DocumentField: c.IsKnownField, DataField: func(string) bool { return true }, ScalarField: c.ScalarField, ArrayField: c.ArrayField,
			EncryptedField: c.IsEncrypted, SearchableField: c.IsSearchable, Roles: diffRoles,
		})
		if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), want) {
			t.Errorf("%s: %v, want %q", src, errs, want)
		}
	}
}

// A query the blind index can't answer fails closed instead of running against ciphertext.
func TestQueriesTheBlindIndexCantAnswerAreRefused(t *testing.T) {
	_, repo := encryptedFixture(t)
	ctx := context.Background()
	insert(t, repo, map[string]any{"code": "a", "secret": "s"}, time.Now(), nil)
	for name, f := range map[string]storage.Filter{
		"not equal":      storage.Condition{Field: "code", Op: storage.OpNe, Value: "a"},
		"range":          storage.Condition{Field: "code", Op: storage.OpGt, Value: "a"},
		"null":           storage.Condition{Field: "code", Op: storage.OpEq, Value: nil},
		"not searchable": storage.Condition{Field: "secret", Op: storage.OpEq, Value: "s"},
		"in with null":   storage.Condition{Field: "code", Op: storage.OpIn, Value: []any{"a", nil}},
		"inside a rule":  storage.And{storage.Condition{Field: "status", Op: storage.OpEq, Value: "x"}, storage.Not{Filter: storage.Condition{Field: "code", Op: storage.OpLike, Value: "a%"}}},
	} {
		for where, q := range map[string]storage.Query{"filter": {Filter: f, Limit: 10}, "access": {Access: f, Limit: 10}} {
			if _, err := repo.List(ctx, q); err == nil || !strings.Contains(err.Error(), "encrypted") {
				t.Errorf("%s as %s: %v", name, where, err)
			}
		}
	}
}
