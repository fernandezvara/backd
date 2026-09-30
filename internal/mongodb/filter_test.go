package mongodb

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

func TestBuildFilterShape(t *testing.T) {
	f, err := buildFilter(storage.And{storage.Condition{Field: "id", Op: storage.OpEq, Value: "x"}})
	want := bson.D{{Key: "_id", Value: bson.D{{Key: "$eq", Value: "x"}}}}
	if err != nil || !reflect.DeepEqual(f, want) {
		t.Errorf("single condition = %v, %v", f, err)
	}

	f, _ = buildFilter(storage.And{
		storage.Condition{Field: "age", Op: storage.OpGt, Value: int64(1)},
		storage.Condition{Field: "age", Op: storage.OpLt, Value: int64(5)},
	})
	want = bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "age", Value: bson.D{{Key: "$gt", Value: int64(1)}}}},
		bson.D{{Key: "age", Value: bson.D{{Key: "$lt", Value: int64(5)}}}},
	}}}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("two conditions = %v", f)
	}

	if f, _ := buildFilter(nil); len(f) != 0 {
		t.Errorf("empty filter = %v", f)
	}
	if _, err := buildFilter(storage.Condition{Field: "a", Op: "$where", Value: "x"}); err == nil {
		t.Error("unknown operator accepted")
	}

	// $or groups: one clause per branch, inside $or.
	f, _ = buildFilter(storage.And{
		storage.Condition{Field: "published", Op: storage.OpEq, Value: true},
		storage.Or{
			storage.And{storage.Condition{Field: "title", Op: storage.OpIContains, Value: "go"}},
			storage.And{storage.Condition{Field: "body", Op: storage.OpIContains, Value: "go"}},
		},
	})
	want = bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "published", Value: bson.D{{Key: "$eq", Value: true}}}},
		bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "title", Value: bson.D{{Key: "$regex", Value: bson.Regex{Pattern: "go", Options: "i"}}}}},
			bson.D{{Key: "body", Value: bson.D{{Key: "$regex", Value: bson.Regex{Pattern: "go", Options: "i"}}}}},
		}}},
	}}}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("or group = %v", f)
	}
}

func TestRegexPatterns(t *testing.T) {
	tests := []struct {
		op      storage.Operator
		in      string
		pattern string
		options string
	}{
		{storage.OpContains, "a.*b", `a\.\*b`, ""},
		{storage.OpIContains, "(x)", `\(x\)`, "i"},
		{storage.OpStartsWith, "^a$", `^\^a\$`, ""},
		{storage.OpEndsWith, "a|b", `a\|b\z`, ""},
		{storage.OpLike, "a%b_c.", `^a.*b.c\.\z`, "s"},
		{storage.OpILike, "[x]%", `^\[x\].*\z`, "si"},
	}
	for _, tt := range tests {
		d, err := clause(storage.Condition{Field: "f", Op: tt.op, Value: tt.in})
		if err != nil {
			t.Fatal(err)
		}
		re := d[0].Value.(bson.D)[0].Value.(bson.Regex)
		if re.Pattern != tt.pattern || re.Options != tt.options {
			t.Errorf("%s %q = /%s/%s, want /%s/%s", tt.op, tt.in, re.Pattern, re.Options, tt.pattern, tt.options)
		}
	}
}

// TestOperatorsAgainstMongoDB checks the results of every operator on real data.
func TestOperatorsAgainstMongoDB(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	fixtures := []map[string]any{
		{"name": "Alice", "age": int64(30), "price": 10.5, "address": map[string]any{"city": "Madrid"}, "tags": []any{"a", "b"}, "note": "x"},
		{"name": "bob", "age": int64(25), "price": int64(20), "address": map[string]any{"city": "Barcelona"}, "tags": []any{"b"}, "note": nil},
		{"name": "Carol.*", "age": int64(35), "price": 7.25, "tags": []any{}},
		{"name": "dave_%\nx", "age": int64(40), "price": int64(5), "address": map[string]any{"city": "madrid"}},
	}
	for i, fx := range fixtures {
		d := newDoc(fx)
		ts := t0.Add(time.Duration(i) * time.Hour)
		d["_meta"] = map[string]any{"created_at": ts, "updated_at": ts, "version": int64(1)}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	c := func(f string, op storage.Operator, v any) storage.Condition {
		return storage.Condition{Field: f, Op: op, Value: v}
	}
	tests := []struct {
		name string
		cond []storage.Condition
		want []string
	}{
		{"eq", []storage.Condition{c("name", storage.OpEq, "bob")}, []string{"bob"}},
		{"eq is case-sensitive", []storage.Condition{c("name", storage.OpEq, "BOB")}, nil},
		{"eq int vs float", []storage.Condition{c("price", storage.OpEq, 20.0)}, []string{"bob"}},
		{"eq nested", []storage.Condition{c("address.city", storage.OpEq, "Madrid")}, []string{"Alice"}},
		{"eq object", []storage.Condition{c("address", storage.OpEq, map[string]any{"city": "Barcelona"})}, []string{"bob"}},
		{"eq array element", []storage.Condition{c("tags", storage.OpEq, "b")}, []string{"Alice", "bob"}},
		{"eq whole array", []storage.Condition{c("tags", storage.OpEq, []any{"b"})}, []string{"bob"}},
		{"eq null matches null and missing", []storage.Condition{c("note", storage.OpEq, nil)}, []string{"bob", "Carol.*", "dave_%\nx"}},
		{"ne", []storage.Condition{c("name", storage.OpNe, "bob")}, []string{"Alice", "Carol.*", "dave_%\nx"}},
		{"ne includes null and missing", []storage.Condition{c("note", storage.OpNe, "x")}, []string{"bob", "Carol.*", "dave_%\nx"}},
		{"gt", []storage.Condition{c("age", storage.OpGt, int64(30))}, []string{"Carol.*", "dave_%\nx"}},
		{"gte", []storage.Condition{c("age", storage.OpGte, int64(30))}, []string{"Alice", "Carol.*", "dave_%\nx"}},
		{"lt", []storage.Condition{c("price", storage.OpLt, 7.25)}, []string{"dave_%\nx"}},
		{"lte", []storage.Condition{c("price", storage.OpLte, 7.25)}, []string{"Carol.*", "dave_%\nx"}},
		{"gt string", []storage.Condition{c("name", storage.OpGt, "C")}, []string{"bob", "Carol.*", "dave_%\nx"}},
		{"range combined", []storage.Condition{c("age", storage.OpGt, int64(25)), c("age", storage.OpLt, int64(40))}, []string{"Alice", "Carol.*"}},
		{"between inclusive", []storage.Condition{c("age", storage.OpBetween, []any{int64(25), int64(35)})}, []string{"Alice", "bob", "Carol.*"}},
		{"in", []storage.Condition{c("name", storage.OpIn, []any{"bob", "Alice", "zed"})}, []string{"Alice", "bob"}},
		{"in empty", []storage.Condition{c("name", storage.OpIn, []any{})}, nil},
		{"nin", []storage.Condition{c("age", storage.OpNin, []any{int64(25), int64(30)})}, []string{"Carol.*", "dave_%\nx"}},
		{"isNull true", []storage.Condition{c("address", storage.OpIsNull, true)}, []string{"Carol.*"}},
		{"isNull false", []storage.Condition{c("note", storage.OpIsNull, false)}, []string{"Alice"}},
		{"like", []storage.Condition{c("name", storage.OpLike, "A%e")}, []string{"Alice"}},
		{"like underscore", []storage.Condition{c("name", storage.OpLike, "b_b")}, []string{"bob"}},
		{"like is anchored", []storage.Condition{c("name", storage.OpLike, "lic")}, nil},
		{"like is case-sensitive", []storage.Condition{c("name", storage.OpLike, "alice")}, nil},
		{"like matches newlines", []storage.Condition{c("name", storage.OpLike, "dave%x")}, []string{"dave_%\nx"}},
		{"ilike", []storage.Condition{c("address.city", storage.OpILike, "MAD%")}, []string{"Alice", "dave_%\nx"}},
		{"startsWith", []storage.Condition{c("name", storage.OpStartsWith, "Car")}, []string{"Carol.*"}},
		{"istartsWith", []storage.Condition{c("name", storage.OpIStartsWith, "car")}, []string{"Carol.*"}},
		{"endsWith", []storage.Condition{c("address.city", storage.OpEndsWith, "id")}, []string{"Alice", "dave_%\nx"}},
		{"endsWith is not fooled by newline", []storage.Condition{c("name", storage.OpEndsWith, "%")}, nil},
		{"iendsWith", []storage.Condition{c("name", storage.OpIEndsWith, "BOB")}, []string{"bob"}},
		{"contains", []storage.Condition{c("name", storage.OpContains, "li")}, []string{"Alice"}},
		{"icontains", []storage.Condition{c("name", storage.OpIContains, "O")}, []string{"bob", "Carol.*"}},
		{"regex metacharacters are literal", []storage.Condition{c("name", storage.OpContains, ".*")}, []string{"Carol.*"}},
		{"like metacharacters are literal", []storage.Condition{c("name", storage.OpLike, "%.*")}, []string{"Carol.*"}},
		{"wildcards in data are literal for startsWith", []storage.Condition{c("name", storage.OpStartsWith, "dave_%")}, []string{"dave_%\nx"}},
		{"id", []storage.Condition{c("id", storage.OpGt, "")}, []string{"Alice", "bob", "Carol.*", "dave_%\nx"}},
		{"created_at", []storage.Condition{c("_meta.created_at", storage.OpGte, t0.Add(2*time.Hour))}, []string{"Carol.*", "dave_%\nx"}},
		{"created_at between", []storage.Condition{c("_meta.created_at", storage.OpBetween, []any{t0, t0.Add(time.Hour)})}, []string{"Alice", "bob"}},
	}
	and := func(conds []storage.Condition) storage.Filter {
		out := storage.And{}
		for _, c := range conds {
			out = append(out, c)
		}
		return out
	}
	list := func(t *testing.T, f storage.Filter, want []string) {
		t.Helper()
		page, err := repo.List(ctx, storage.Query{Filter: f, Limit: 100})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var got []string
		for _, d := range page.Items {
			got = append(got, d["name"].(string))
		}
		slices.Sort(got)
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { list(t, and(tt.cond), tt.want) })
	}

	// Groups: $or of conditions on different fields, several conditions on
	// one field across branches, nesting, and combination with AND.
	for _, tt := range []struct {
		name string
		f    storage.Filter
		want []string
	}{
		{"or on two fields", storage.Or{and([]storage.Condition{c("name", storage.OpIContains, "car")}), and([]storage.Condition{c("address.city", storage.OpEq, "Barcelona")})}, []string{"bob", "Carol.*"}},
		{"or on one field", storage.Or{and([]storage.Condition{c("age", storage.OpLt, int64(26))}), and([]storage.Condition{c("age", storage.OpGt, int64(39))})}, []string{"bob", "dave_%\nx"}},
		{"and with or", storage.And{c("tags", storage.OpEq, "b"), storage.Or{and([]storage.Condition{c("age", storage.OpEq, int64(30))}), and([]storage.Condition{c("note", storage.OpIsNull, true)})}}, []string{"Alice", "bob"}},
		{"nested", storage.Or{
			and([]storage.Condition{c("name", storage.OpEq, "Alice")}),
			storage.And{storage.Or{and([]storage.Condition{c("price", storage.OpLt, int64(6))}), and([]storage.Condition{c("price", storage.OpGt, int64(15))})}, c("age", storage.OpGte, int64(40))},
		}, []string{"Alice", "dave_%\nx"}},
		{"or with a missing field", storage.Or{and([]storage.Condition{c("note", storage.OpEq, "x")}), and([]storage.Condition{c("address", storage.OpIsNull, true)})}, []string{"Alice", "Carol.*"}},
	} {
		t.Run(tt.name, func(t *testing.T) { list(t, tt.f, tt.want) })
	}
}

func TestSortAndCountAgainstMongoDB(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	for _, fx := range []map[string]any{
		{"n": "a", "g": int64(2)}, {"n": "b", "g": int64(1)}, {"n": "c", "g": int64(2)}, {"n": "d", "g": int64(1)}, {"n": "e"},
	} {
		if err := repo.Create(ctx, newDoc(fx)); err != nil {
			t.Fatal(err)
		}
	}
	names := func(p storage.Page) (out []string) {
		for _, d := range p.Items {
			out = append(out, d["n"].(string))
		}
		return out
	}

	// g descending, ties broken by id ascending (creation order); missing sorts last in desc.
	page, err := repo.List(ctx, storage.Query{Sort: []storage.SortField{{Field: "g", Desc: true}, {Field: "id"}}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(page); !slices.Equal(got, []string{"a", "c", "b", "d", "e"}) {
		t.Errorf("sort = %v", got)
	}

	page, _ = repo.List(ctx, storage.Query{Sort: []storage.SortField{{Field: "id", Desc: true}}, Limit: 2, Skip: 1})
	if got := names(page); !slices.Equal(got, []string{"d", "c"}) || !page.HasMore {
		t.Errorf("id desc page = %v more=%v", got, page.HasMore)
	}
	if page.Total != nil {
		t.Error("total set without Count")
	}

	page, _ = repo.List(ctx, storage.Query{
		Filter: storage.Condition{Field: "g", Op: storage.OpEq, Value: int64(2)},
		Limit:  1, Count: true,
	})
	if page.Total == nil || *page.Total != 2 || len(page.Items) != 1 || !page.HasMore {
		t.Errorf("count page = %v total=%v", names(page), page.Total)
	}
}

func TestBuildQueryAccess(t *testing.T) {
	owner := storage.Condition{Field: "_meta.owner", Op: storage.OpEq, Value: "u1"}
	pub := storage.Condition{Field: "published", Op: storage.OpEq, Value: true}
	tests := []struct {
		name string
		q    storage.Query
		want string
	}{
		{"no access", storage.Query{}, `{}`},
		{"access only", storage.Query{Access: owner}, `{"_meta.owner": {"$eq": "u1"}}`},
		{"with where", storage.Query{Filter: storage.And{pub}, Access: owner},
			`{"$and": [{"published": {"$eq": true}}, {"_meta.owner": {"$eq": "u1"}}]}`},
		{"where with $or", storage.Query{Filter: storage.Or{pub, storage.Not{Filter: pub}}, Access: owner},
			`{"$and": [{"$or": [{"published": {"$eq": true}}, {"$nor": [{"published": {"$eq": true}}]}]}, {"_meta.owner": {"$eq": "u1"}}]}`},
		{"or", storage.Query{Access: storage.Or{pub, owner}},
			`{"$or": [{"published": {"$eq": true}}, {"_meta.owner": {"$eq": "u1"}}]}`},
		{"not", storage.Query{Access: storage.Not{Filter: pub}}, `{"$nor": [{"published": {"$eq": true}}]}`},
		{"nothing", storage.Query{Access: storage.Const(false)}, `{"_id": {"$in": []}}`},
		{"everything", storage.Query{Access: storage.And{storage.Const(true)}}, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildQuery(tt.q)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := bson.MarshalExtJSON(got, false, false)
			var gotV, wantV any
			_ = bson.UnmarshalExtJSON(b, false, &gotV)
			_ = bson.UnmarshalExtJSON([]byte(tt.want), false, &wantV)
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("got %s, want %s", b, tt.want)
			}
		})
	}
}
