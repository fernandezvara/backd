package mongodb

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Realtime decides what a subscriber is told by evaluating `where` AND the read rule in
// memory (storage.Match) on the document before and after a change
// (design/triggers-and-realtime.md §18). That is only right if Match agrees with what
// MongoDB lists for the same filter, arrays, missing fields and nulls included: the events
// must be what a list before and after the change would show. This is that comparison, for
// the operators a subscription may use, combined with the read rules of TestReadRuleFiltersMatchRules.

// subscriptionWheres are the filters a subscription may carry, over the fields of diffSchema.
var subscriptionWheres = []storage.Filter{
	nil,
	storage.Condition{Field: "status", Op: storage.OpEq, Value: "a"},
	storage.Condition{Field: "status", Op: storage.OpNe, Value: "a"},
	storage.Condition{Field: "status", Op: storage.OpIn, Value: []any{"a", "b"}},
	storage.Condition{Field: "status", Op: storage.OpNin, Value: []any{"a", "b"}},
	storage.Condition{Field: "n", Op: storage.OpGt, Value: int64(3)},
	storage.Condition{Field: "n", Op: storage.OpLte, Value: int64(3)},
	storage.Condition{Field: "n", Op: storage.OpBetween, Value: []any{int64(1), int64(5)}},
	storage.Condition{Field: "score", Op: storage.OpGte, Value: 1.5},
	storage.Condition{Field: "published", Op: storage.OpEq, Value: true},
	storage.Condition{Field: "published", Op: storage.OpNe, Value: true},
	storage.Condition{Field: "tags", Op: storage.OpEq, Value: "x"},
	storage.Condition{Field: "tags", Op: storage.OpNe, Value: "x"},
	storage.Condition{Field: "tags", Op: storage.OpIn, Value: []any{"x", "z"}},
	storage.Condition{Field: "tags", Op: storage.OpNin, Value: []any{"x"}},
	storage.Condition{Field: "members", Op: storage.OpEq, Value: "u1"},
	storage.Condition{Field: "opt", Op: storage.OpIsNull, Value: true},
	storage.Condition{Field: "opt", Op: storage.OpIsNull, Value: false},
	storage.Condition{Field: "address.city", Op: storage.OpEq, Value: "Madrid"},
	storage.Condition{Field: "address.city", Op: storage.OpIsNull, Value: true},
	storage.Condition{Field: "_meta.owner", Op: storage.OpEq, Value: "u1"},
	storage.Condition{Field: "_meta.owner", Op: storage.OpIsNull, Value: true},
	storage.Condition{Field: "_meta.owner", Op: storage.OpIn, Value: []any{"u1", "u2"}},
	storage.And{
		storage.Condition{Field: "status", Op: storage.OpIn, Value: []any{"a", "b", "c"}},
		storage.Condition{Field: "n", Op: storage.OpGte, Value: int64(1)},
	},
	storage.Or{
		storage.Condition{Field: "published", Op: storage.OpEq, Value: true},
		storage.Condition{Field: "tags", Op: storage.OpEq, Value: "y"},
	},
	storage.Not{Filter: storage.Condition{Field: "status", Op: storage.OpEq, Value: "a"}},
	storage.Not{Filter: storage.Or{
		storage.Condition{Field: "tags", Op: storage.OpEq, Value: "x"},
		storage.Condition{Field: "n", Op: storage.OpGt, Value: int64(4)},
	}},
}

func TestSubscriptionFiltersMatchMongoDB(t *testing.T) {
	c, repo := diffFixture(t)
	rng := rand.New(rand.NewPCG(11, 17))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for range 300 {
		fields := map[string]any{
			"status":    pick("a", "b", "c", "public", ""),
			"n":         pick(int64(0), int64(1), int64(3), int64(4), int64(5), int64(10)),
			"score":     pick(0.5, 1.0, 1.5, 2.25),
			"published": pick(true, false),
			"tags":      pick([]any{}, []any{"x"}, []any{"x", "y"}, []any{"y"}),
			"members":   pick([]any{}, []any{"u1"}, []any{"u2", "u1"}, []any{"u3"}),
			"address":   map[string]any{"city": pick("Madrid", "Oslo", "")},
			"opt":       pick("s", nil),
		}
		if rng.IntN(5) == 0 { // written before the field existed
			delete(fields, pick("status", "n", "score", "published", "tags", "members", "address", "opt").(string))
		}
		insert(t, repo, fields, now.Add(-time.Duration(rng.IntN(120))*time.Minute), pick("u1", "u2", "u3", nil))
	}
	docs := all(t, repo, nil)

	readRules := []string{`user != nil`, `document.published == true || (user != nil && document._meta.owner == user.id)`,
		`"user != nil && user.id in document.members"`, `"'x' in document.tags"`, `"true"`}
	var checked int
	for _, src := range readRules {
		rule := compileRead(t, c, src)
		for name, u := range diffUsers {
			rf, err := rule.Filter(rules.Values{User: u, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			for i, where := range subscriptionWheres {
				f := storage.Simplify(storage.And{where, rf})
				if where == nil {
					f = rf
				}
				var inMemory []string
				for id, d := range docs {
					ok, err := storage.Match(d, f)
					if err != nil {
						t.Fatalf("%s as %s, where #%d: %v", src, name, i, err)
					}
					if ok {
						inMemory = append(inMemory, id)
					}
				}
				slices.Sort(inMemory)
				mongo := listedBy(t, repo, f)
				slices.Sort(mongo)
				if !slices.Equal(inMemory, mongo) {
					t.Errorf("%s as %s, where #%d (%v): in memory %d, MongoDB %d\n  only in memory: %v\n  only in MongoDB: %v",
						src, name, i, where, len(inMemory), len(mongo), diff(inMemory, mongo), diff(mongo, inMemory))
				}
				checked++
			}
		}
	}
	t.Logf("%d (rule, caller, where) combinations compared over %d documents", checked, len(docs))
}
