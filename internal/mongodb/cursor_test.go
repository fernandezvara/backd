package mongodb

import (
	"context"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/storage"
)

// TestCursorPagingOnMongoDB walks a collection page by page with cursors, in
// many orders, with duplicate, null and missing sort values, and checks that
// the pages are exactly the full list in order: nothing repeated, nothing lost.
func TestCursorPagingOnMongoDB(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	log, _ := testLogger()
	reg := loadRegistry(t, realm, map[string]string{"app/items": `{"type":"object","properties":{
		"name":{"type":["string","null"]}, "score":{"type":"integer"}, "ratio":{"type":"number"},
		"flag":{"type":"boolean"}, "tags":{"type":"array","items":{"type":"string"}},
		"profile":{"type":"object","properties":{"city":{"type":"string"}}}}}`})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "items")
	repo := (&Store{Client: client}).Repository(c)
	ctx := context.Background()

	rng := rand.New(rand.NewSource(7))
	names := []any{"ada", "bob", "ada", "carl", nil, "bob", "", "Zed"}
	base := time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC)
	const total = 60
	for i := 0; i < total; i++ {
		doc := newDoc(map[string]any{"score": int64(rng.Intn(5)), "ratio": float64(rng.Intn(4)) / 2, "flag": rng.Intn(2) == 0})
		switch n := names[rng.Intn(len(names))]; n {
		case nil:
			if rng.Intn(2) == 0 {
				doc["name"] = nil // explicit null; otherwise the field is missing
			}
		default:
			doc["name"] = n
		}
		if rng.Intn(3) > 0 {
			doc["profile"] = map[string]any{"city": []string{"Oslo", "Lima", "Kyiv"}[rng.Intn(3)]}
		}
		at := base.Add(time.Duration(rng.Intn(6)) * time.Second) // many equal timestamps
		doc["_meta"] = map[string]any{"created_at": at, "updated_at": at, "version": int64(1)}
		if err := repo.Create(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(docs []storage.Document) []string {
		out := make([]string, len(docs))
		for i, d := range docs {
			out[i] = d["id"].(string)
		}
		return out
	}
	for _, order := range []string{
		"", "id", "-id", "name", "-name", "name,-score", "-score,name", "flag,-name", "ratio,-ratio2x",
		"_meta.created_at", "-_meta.created_at", "-_meta.created_at,score", "score,_meta.created_at,-name", "profile.city,-name", "-profile.city",
	} {
		if order == "ratio,-ratio2x" {
			order = "ratio,-score"
		}
		t.Run("order_by="+order, func(t *testing.T) {
			sort, err := query.ParseOrderBy(order, c.Fields)
			if err != nil {
				t.Fatal(err)
			}
			if ok, field := query.Cursorable(sort, c.Fields); !ok {
				t.Fatalf("not cursorable at %s", field)
			}
			all, err := repo.List(ctx, storage.Query{Sort: sort, Limit: 1000})
			if err != nil || len(all.Items) != total {
				t.Fatalf("full list: %d items, %v", len(all.Items), err)
			}
			for _, limit := range []int{1, 7, 25} {
				var got []storage.Document
				var after storage.Filter
				for pages := 0; ; pages++ {
					if pages > total+1 {
						t.Fatalf("limit %d: too many pages", limit)
					}
					page, err := repo.List(ctx, storage.Query{Sort: sort, Limit: limit, After: after})
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, page.Items...)
					if !page.HasMore {
						break
					}
					cursor := query.EncodeCursor(sort, page.Items[len(page.Items)-1])
					if after, err = query.ParseCursor(cursor, sort, c.Fields); err != nil {
						t.Fatal(err)
					}
				}
				if !slices.Equal(ids(got), ids(all.Items)) {
					t.Fatalf("limit %d: pages differ from the full list\n got %v\nwant %v", limit, ids(got), ids(all.Items))
				}
			}
		})
	}

	// A filter, and a cursor of a document that is deleted afterwards.
	sort, _ := query.ParseOrderBy("-score,name", c.Fields)
	filter, err := query.ParseWhere(`{"flag": true}`, c.Fields)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := repo.List(ctx, storage.Query{Filter: filter, Sort: sort, Limit: 5})
	last := first.Items[len(first.Items)-1]
	cursor := query.EncodeCursor(sort, last)
	if err := repo.Delete(ctx, last["id"].(string), nil); err != nil {
		t.Fatal(err)
	}
	after, err := query.ParseCursor(cursor, sort, c.Fields)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := repo.List(ctx, storage.Query{Filter: filter, Sort: sort, Limit: 1000, After: after})
	if err != nil {
		t.Fatal(err)
	}
	everything, _ := repo.List(ctx, storage.Query{Filter: filter, Sort: sort, Limit: 1000})
	want := ids(everything.Items)[len(first.Items)-1:] // the deleted one is gone: the rest starts where it was
	if !slices.Equal(ids(rest.Items), want) {
		t.Errorf("after a deleted document:\n got %v\nwant %v", ids(rest.Items), want)
	}
	// The count ignores the cursor.
	counted, err := repo.List(ctx, storage.Query{Filter: filter, Sort: sort, Limit: 2, After: after, Count: true})
	if err != nil || counted.Total == nil || int(*counted.Total) != len(everything.Items) {
		t.Errorf("total with a cursor = %v, want %d (%v)", counted.Total, len(everything.Items), err)
	}
}
