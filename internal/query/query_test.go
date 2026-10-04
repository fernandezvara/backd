package query

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

var fields = map[string]registry.Field{
	"name":         {Types: []string{"string"}},
	"age":          {Types: []string{"integer"}},
	"price":        {Types: []string{"number"}},
	"active":       {Types: []string{"boolean"}},
	"note":         {Types: []string{"string", "null"}},
	"address":      {Types: []string{"object"}},
	"address.city": {Types: []string{"string"}},
	"tags":         {Types: []string{"array"}, ItemTypes: []string{"string"}},
	"anything":     {},
}

func cond(f string, op storage.Operator, v any) storage.Condition {
	return storage.Condition{Field: f, Op: op, Value: v}
}

func TestParseWhereValid(t *testing.T) {
	ts := time.Date(2026, 9, 25, 21, 30, 0, 0, time.UTC)
	tests := []struct {
		where string
		want  []storage.Condition
	}{
		{``, nil},
		{`{}`, []storage.Condition{}},
		{`{"name": "a"}`, []storage.Condition{cond("name", storage.OpEq, "a")}},
		{`{"name": {"$eq": "a"}}`, []storage.Condition{cond("name", storage.OpEq, "a")}},
		{`{"age": {"$gt": 1, "$lte": 9}}`, []storage.Condition{cond("age", storage.OpGt, int64(1)), cond("age", storage.OpLte, int64(9))}},
		{`{"age": 2.0}`, []storage.Condition{cond("age", storage.OpEq, int64(2))}}, // whole numbers become int64; MongoDB compares numbers across types
		{`{"price": {"$between": [1, 2.5]}}`, []storage.Condition{cond("price", storage.OpBetween, []any{int64(1), 2.5})}},
		{`{"name": {"$in": ["a", "b"]}, "age": {"$nin": []}}`, []storage.Condition{cond("age", storage.OpNin, []any{}), cond("name", storage.OpIn, []any{"a", "b"})}},
		{`{"active": {"$ne": true}}`, []storage.Condition{cond("active", storage.OpNe, true)}},
		{`{"note": null}`, []storage.Condition{cond("note", storage.OpEq, nil)}},
		{`{"note": {"$isNull": true}, "name": {"$notNull": true}}`, []storage.Condition{cond("name", storage.OpIsNull, false), cond("note", storage.OpIsNull, true)}},
		{`{"address.city": {"$ilike": "ma%d_"}}`, []storage.Condition{cond("address.city", storage.OpILike, "ma%d_")}},
		{`{"name": {"$startsWith": "a", "$iendsWith": "Z", "$contains": ".*"}}`, []storage.Condition{
			cond("name", storage.OpContains, ".*"), cond("name", storage.OpIEndsWith, "Z"), cond("name", storage.OpStartsWith, "a")}},
		{`{"address": {"city": "X"}}`, []storage.Condition{cond("address", storage.OpEq, map[string]any{"city": "X"})}},
		{`{"tags": "a"}`, []storage.Condition{cond("tags", storage.OpEq, "a")}},
		{`{"tags": ["a", "b"]}`, []storage.Condition{cond("tags", storage.OpEq, []any{"a", "b"})}},
		{`{"anything": {"$gt": 3}}`, []storage.Condition{cond("anything", storage.OpGt, int64(3))}},
		{`{"id": {"$in": ["x"]}}`, []storage.Condition{cond("id", storage.OpIn, []any{"x"})}},
		{`{"_meta.version": {"$gt": 2}}`, []storage.Condition{cond("_meta.version", storage.OpGt, int64(2))}},
		{`{"_meta.created_at": {"$gte": "2026-09-25T23:30:00+02:00"}}`, []storage.Condition{cond("_meta.created_at", storage.OpGte, ts)}},
	}
	for _, tt := range tests {
		t.Run(tt.where, func(t *testing.T) {
			got, err := ParseWhere(tt.where, fields)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tt.want) == 0 {
				if got != nil {
					t.Errorf("got %#v, want no filter", got)
				}
				return
			}
			want := storage.And{}
			for _, c := range tt.want {
				want = append(want, c)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %#v\nwant %#v", got, want)
			}
		})
	}
}

func TestParseWhereInvalid(t *testing.T) {
	long := `"` + strings.Repeat("a", MaxTextLen+1) + `"`
	// 33 distinct conditions: 11 operators on each of three numeric fields.
	var many []string
	for _, f := range []string{"age", "price", "anything"} {
		many = append(many, `"`+f+`": {"$eq": 1, "$ne": 1, "$gt": 1, "$gte": 1, "$lt": 1, "$lte": 1, "$in": [1], "$nin": [1], "$between": [1, 2], "$isNull": false, "$notNull": true}`)
	}
	tests := []struct {
		where, path, reason string
	}{
		{`[1]`, "where", "valid JSON object"},
		{`{`, "where", "valid JSON object"},
		{`{"a":1} {}`, "where", "valid JSON object"},
		{`{"a": ` + strings.Repeat(" ", MaxWhereBytes) + `1}`, "where", "at most"},
		{`{"$nor": [{"name": "a"}]}`, "where.$nor", "not supported"},
		{`{"$where": "sleep(1000)"}`, "where.$where", "not supported"},
		{`{"$or": []}`, "where.$or", "non-empty array"},
		{`{"$or": {"name": "a"}}`, "where.$or", "non-empty array"},
		{`{"$or": [{}]}`, "where.$or.0", "non-empty where object"},
		{`{"$and": ["name"]}`, "where.$and.0", "non-empty where object"},
		{`{"$or": [{"name": "a"}, {"secret": 1}]}`, "where.$or.1.secret", "unknown field"},
		{`{"$or": [{"age": {"$gt": "x"}}]}`, "where.$or.0.age.$gt", "integer"},
		{`{"$or": [{"$and": [{"$or": [{"$and": [{"$or": [{"name": "a"}]}]}]}]}]}`, "where.$or.0.$and.0.$or.0.$and.0.$or", "nested at most 4"},
		{`{"$or": [{"$where": "1"}]}`, "where.$or.0.$where", "not supported"},
		{`{"secret": 1}`, "where.secret", "unknown field"},
		{`{"tags.0": "a"}`, "where.tags.0", "unknown field"},
		{`{"_id": "x"}`, "where._id", "unknown field"},
		{`{"_meta.version": "1"}`, "where._meta.version.$eq", "integer"},
		{`{"_meta.other": 1}`, "where._meta.other", "unknown field"},
		{`{"name": {"$regex": ".*"}}`, "where.name.$regex", "unknown operator"},
		{`{"name": {"$expr": {}}}`, "where.name.$expr", "unknown operator"},
		{`{"name": {"$eq": "a", "x": 1}}`, "where.name", "cannot mix"},
		{`{"address": {"city": {"$ne": "x"}}}`, "where.address.$eq", "must not start with '$'"},
		{`{"address": {"$eq": {"city": {"$gt": ""}}}}`, "where.address.$eq", "must not start with '$'"},
		{`{"age": "1"}`, "where.age.$eq", "integer"},
		{`{"age": 1.5}`, "where.age.$eq", "integer"},
		{`{"name": 1}`, "where.name.$eq", "string"},
		{`{"name": null}`, "where.name.$eq", "$isNull"},
		{`{"active": {"$gt": true}}`, "where.active.$gt", "number or a string"},
		{`{"age": {"$gt": "a"}}`, "where.age.$gt", "integer"},
		{`{"age": {"$between": [1]}}`, "where.age.$between", "two values"},
		{`{"age": {"$in": 1}}`, "where.age.$in", "array"},
		{`{"age": {"$in": [1, "x"]}}`, "where.age.$in.1", "integer"},
		{`{"tags": 1}`, "where.tags.$eq", "item of type string"},
		{`{"note": {"$isNull": 1}}`, "where.note.$isNull", "true or false"},
		{`{"age": {"$like": "1%"}}`, "where.age.$like", "only to string fields"},
		{`{"name": {"$like": 1}}`, "where.name.$like", "must be a string"},
		{`{"name": {"$like": "a%b%c%d%e%"}}`, "where.name.$like", "wildcards"},
		{`{"name": {"$contains": ` + long + `}}`, "where.name.$contains", "at most"},
		{`{"_meta.created_at": {"$gt": "yesterday"}}`, "where._meta.created_at.$gt", "RFC3339"},
		{`{"_meta.updated_at": {"$contains": "2026"}}`, "where._meta.updated_at.$contains", "only to string fields"},
		{`{"id": 5}`, "where.id.$eq", "string"},
		{`{` + strings.Join(many, ",") + `}`, "where", "at most 32 conditions"},
	}
	for _, tt := range tests {
		t.Run(tt.where[:min(len(tt.where), 60)], func(t *testing.T) {
			_, err := ParseWhere(tt.where, fields)
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error = %v, want query.Errors", err)
			}
			for _, e := range errs {
				if e.Path == tt.path && strings.Contains(e.Reason, tt.reason) {
					return
				}
			}
			t.Errorf("errors %v do not include %s: …%s…", errs, tt.path, tt.reason)
		})
	}
}

func TestParseWhereGroups(t *testing.T) {
	title := cond("name", storage.OpIContains, "go")
	body := cond("note", storage.OpIContains, "go")
	tests := []struct {
		where string
		want  storage.Filter
	}{
		{`{"$or": [{"name": {"$icontains": "go"}}, {"note": {"$icontains": "go"}}]}`,
			storage.And{storage.Or{storage.And{title}, storage.And{body}}}},
		{`{"active": true, "$or": [{"name": {"$icontains": "go"}}, {"note": {"$icontains": "go"}}]}`,
			storage.And{storage.Or{storage.And{title}, storage.And{body}}, cond("active", storage.OpEq, true)}},
		{`{"$and": [{"age": {"$gt": 1}}, {"age": {"$lt": 9}}]}`,
			storage.And{storage.And{storage.And{cond("age", storage.OpGt, int64(1))}, storage.And{cond("age", storage.OpLt, int64(9))}}}},
		{`{"$or": [{"age": 1, "active": true}, {"$and": [{"name": "a"}, {"$or": [{"note": null}, {"tags": "x"}]}]}]}`,
			storage.And{storage.Or{
				storage.And{cond("active", storage.OpEq, true), cond("age", storage.OpEq, int64(1))},
				storage.And{storage.And{
					storage.And{cond("name", storage.OpEq, "a")},
					storage.And{storage.Or{storage.And{cond("note", storage.OpEq, nil)}, storage.And{cond("tags", storage.OpEq, "x")}}},
				}},
			}}},
	}
	for _, tt := range tests {
		t.Run(tt.where, func(t *testing.T) {
			got, err := ParseWhere(tt.where, fields)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}

	// Conditions count across groups.
	var many []string
	for range 33 {
		many = append(many, `{"age": 1}`)
	}
	if _, err := ParseWhere(`{"$or": [`+strings.Join(many, ",")+`]}`, fields); err == nil || !strings.Contains(err.Error(), "at most 32 conditions") {
		t.Errorf("33 conditions in $or: %v", err)
	}
}

func TestParseWhereReportsAllErrors(t *testing.T) {
	_, err := ParseWhere(`{"x": 1, "y": 2, "age": "z"}`, fields)
	if errs, ok := err.(Errors); !ok || len(errs) != 3 {
		t.Errorf("got %v, want 3 errors", err)
	}
}

func TestParseOrderBy(t *testing.T) {
	tests := []struct {
		in   string
		want []storage.SortField
	}{
		{"", []storage.SortField{{Field: "id"}}},
		{"-age,name", []storage.SortField{{Field: "age", Desc: true}, {Field: "name"}, {Field: "id"}}},
		{"address.city, -_meta.created_at", []storage.SortField{{Field: "address.city"}, {Field: "_meta.created_at", Desc: true}, {Field: "id"}}},
		{"-id", []storage.SortField{{Field: "id", Desc: true}}},
	}
	for _, tt := range tests {
		got, err := ParseOrderBy(tt.in, fields)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseOrderBy(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"secret", "age,,name", "age,-age", "-", "$where", "a,b,c,d,e,f,g,h,i"} {
		if _, err := ParseOrderBy(bad, fields); err == nil {
			t.Errorf("ParseOrderBy(%q) accepted", bad)
		}
	}
}

func TestParseCount(t *testing.T) {
	for in, want := range map[string]bool{"": false, "false": false, "true": true} {
		if got, err := ParseCount(in); err != nil || got != want {
			t.Errorf("ParseCount(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseCount("yes"); err == nil {
		t.Error("ParseCount(yes) accepted")
	}
}

func TestParseWhereOnDateFields(t *testing.T) {
	dateFields := map[string]registry.Field{
		"starts_at": {Types: []string{"string"}, Date: true},
		"ends_at":   {Types: []string{"string", "null"}, Date: true},
		"note":      {Types: []string{"string"}}, // a date-time left as text
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	got, err := ParseWhere(`{"starts_at": {"$gte": "2026-09-01T14:00:00+02:00", "$lt": "2026-10-01T00:00:00Z"}, "ends_at": null}`, dateFields)
	if err != nil {
		t.Fatal(err)
	}
	want := storage.And{
		cond("ends_at", storage.OpEq, nil),
		cond("starts_at", storage.OpGte, at), // any offset, compared in UTC
		cond("starts_at", storage.OpLt, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if got, err := ParseWhere(`{"starts_at": {"$between": ["2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"]}, "ends_at": {"$in": ["2026-01-01T00:00:00Z", null]}}`, dateFields); err != nil || got == nil {
		t.Errorf("between and in: %v %v", got, err)
	}
	for _, bad := range []string{
		`{"starts_at": "yesterday"}`,        // not RFC 3339
		`{"starts_at": 5}`,                  // not a string
		`{"starts_at": null}`,               // not nullable
		`{"starts_at": {"$like": "2026%"}}`, // text operators are for text
		`{"starts_at": {"$startsWith": "2026"}}`,
	} {
		if _, err := ParseWhere(bad, dateFields); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	// Left as text it is still text: strings compare as strings.
	if got, err := ParseWhere(`{"note": {"$startsWith": "2026"}}`, dateFields); err != nil || got == nil {
		t.Errorf("text: %v %v", got, err)
	}
}
