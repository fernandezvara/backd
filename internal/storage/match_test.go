package storage

import (
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	doc := Document{
		"id": "d1", "status": "a", "n": int64(5), "score": 1.5, "published": true, "opt": nil,
		"tags": []any{"x", "y"}, "address": map[string]any{"city": "Oslo"},
		"_meta": map[string]any{"owner": "u1", "created_at": at, "version": int64(1)},
	}
	c := func(f string, op Operator, v any) Condition { return Condition{Field: f, Op: op, Value: v} }
	for name, tt := range map[string]struct {
		f    Filter
		want bool
	}{
		"eq":                    {c("status", OpEq, "a"), true},
		"eq other":              {c("status", OpEq, "b"), false},
		"ne":                    {c("status", OpNe, "b"), true},
		"missing equals nil":    {c("nope", OpEq, nil), true},
		"missing ne value":      {c("nope", OpNe, "a"), true},
		"missing is not eq":     {c("nope", OpEq, "a"), false},
		"null is null":          {c("opt", OpIsNull, true), true},
		"missing is null":       {c("nope", OpIsNull, true), true},
		"set is not null":       {c("status", OpIsNull, false), true},
		"array element eq":      {c("tags", OpEq, "x"), true},
		"array element ne":      {c("tags", OpNe, "x"), false},
		"in":                    {c("status", OpIn, []any{"b", "a"}), true},
		"nin":                   {c("status", OpNin, []any{"b"}), true},
		"missing nin":           {c("nope", OpNin, []any{"b"}), true},
		"gt":                    {c("n", OpGt, int64(4)), true},
		"gt equal":              {c("n", OpGt, int64(5)), false},
		"gte equal":             {c("n", OpGte, int64(5)), true},
		"int vs float":          {c("n", OpEq, 5.0), true},
		"float lt int":          {c("score", OpLt, int64(2)), true},
		"missing gt":            {c("nope", OpGt, int64(1)), false},
		"missing not gt":        {Not{c("nope", OpGt, int64(1))}, true},
		"kind mismatch":         {c("status", OpGt, int64(1)), false},
		"string lt":             {c("status", OpLt, "b"), true},
		"time gt":               {c("_meta.created_at", OpGt, at.Add(-time.Hour)), true},
		"time lte":              {c("_meta.created_at", OpLte, at), true},
		"nested":                {c("address.city", OpEq, "Oslo"), true},
		"through a scalar":      {c("status.x", OpEq, nil), true},
		"array any element gt":  {c("tags", OpGt, "w"), true},
		"between":               {c("n", OpBetween, []any{int64(5), int64(9)}), true},
		"between outside":       {c("n", OpBetween, []any{int64(6), int64(9)}), false},
		"between dates":         {c("_meta.created_at", OpBetween, []any{at.Add(-time.Hour), at.Add(time.Hour)}), true},
		"between kind mismatch": {c("status", OpBetween, []any{int64(1), int64(2)}), false},
		"and":                   {And{c("status", OpEq, "a"), c("n", OpGt, int64(1))}, true},
		"and false":             {And{c("status", OpEq, "a"), c("n", OpGt, int64(9))}, false},
		"or":                    {Or{c("status", OpEq, "z"), c("published", OpEq, true)}, true},
		"empty or":              {Or{}, false},
		"empty and":             {And{}, true},
		"const":                 {Const(false), false},
		"nil filter":            {nil, true},
	} {
		got, err := Match(doc, tt.f)
		if err != nil || got != tt.want {
			t.Errorf("%s: %v, %v; want %v", name, got, err, tt.want)
		}
	}
	for _, f := range []Filter{c("status", OpLike, "a%"), c("n", OpBetween, []any{int64(1)}), c("n", OpIn, int64(1)), c("n", OpIsNull, "yes")} {
		if _, err := Match(doc, f); err == nil {
			t.Errorf("%v: want an error", f)
		}
	}
}
