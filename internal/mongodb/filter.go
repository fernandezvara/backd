package mongodb

import (
	"fmt"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

// comparisonOps map directly onto the MongoDB operator of the same name.
var comparisonOps = map[storage.Operator]string{
	storage.OpEq: "$eq", storage.OpNe: "$ne",
	storage.OpGt: "$gt", storage.OpGte: "$gte", storage.OpLt: "$lt", storage.OpLte: "$lte",
	storage.OpIn: "$in", storage.OpNin: "$nin",
}

// mongoField maps an API field path to the stored path.
func mongoField(f string) string {
	if f == "id" {
		return "_id"
	}
	return f
}

// buildFilter translates the client's filter tree (nil: everything).
// Every condition becomes its own clause inside $and / $or, so several
// conditions on one field never collide. Values are always wrapped in an
// explicit operator, so no client value is ever interpreted as a query
// document.
func buildFilter(f storage.Filter) (bson.D, error) {
	if f == nil {
		return bson.D{}, nil
	}
	return buildTree(storage.Simplify(f))
}

// buildQuery combines the client's filter with the access filter.
func buildQuery(q storage.Query) (bson.D, error) {
	filter, err := buildFilter(q.Filter)
	if err != nil || q.Access == nil {
		return filter, err
	}
	access, err := buildTree(storage.Simplify(q.Access))
	if err != nil {
		return nil, err
	}
	if len(filter) == 0 {
		return access, nil
	}
	return bson.D{{Key: "$and", Value: bson.A{filter, access}}}, nil
}

// buildTree translates a filter tree: the client's where, or an access
// rule's filter.
func buildTree(f storage.Filter) (bson.D, error) {
	switch f := f.(type) {
	case storage.Condition:
		return clause(f)
	case storage.Const:
		if f {
			return bson.D{}, nil
		}
		return bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: bson.A{}}}}}, nil
	case storage.Not:
		inner, err := buildTree(f.Filter)
		if err != nil {
			return nil, err
		}
		return bson.D{{Key: "$nor", Value: bson.A{inner}}}, nil
	case storage.And, storage.Or:
		op, parts := "$and", []storage.Filter(nil)
		if a, ok := f.(storage.And); ok {
			parts = a
		} else {
			op, parts = "$or", f.(storage.Or)
		}
		clauses := bson.A{}
		for _, p := range parts {
			c, err := buildTree(p)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, c)
		}
		if len(clauses) == 0 {
			return buildTree(storage.Const(op == "$and"))
		}
		return bson.D{{Key: op, Value: clauses}}, nil
	}
	return nil, fmt.Errorf("unknown filter %T", f)
}

func clause(c storage.Condition) (bson.D, error) {
	f := mongoField(c.Field)
	with := func(op string, v any) bson.D {
		return bson.D{{Key: f, Value: bson.D{{Key: op, Value: v}}}}
	}

	if op, ok := comparisonOps[c.Op]; ok {
		return with(op, toBSONValue(c.Value)), nil
	}
	switch c.Op {
	case storage.OpBetween:
		bounds, ok := c.Value.([]any)
		if !ok || len(bounds) != 2 {
			return nil, fmt.Errorf("$between on %s: want [min, max]", c.Field)
		}
		return bson.D{{Key: f, Value: bson.D{
			{Key: "$gte", Value: toBSONValue(bounds[0])},
			{Key: "$lte", Value: toBSONValue(bounds[1])},
		}}}, nil
	case storage.OpIsNull:
		if isNull, _ := c.Value.(bool); isNull {
			return with("$eq", nil), nil // matches null and missing
		}
		return with("$ne", nil), nil
	}

	s, ok := c.Value.(string)
	if !ok {
		return nil, fmt.Errorf("%s on %s: want a string", c.Op, c.Field)
	}
	var pattern, options string
	switch c.Op {
	case storage.OpContains, storage.OpIContains:
		pattern = regexp.QuoteMeta(s)
	case storage.OpStartsWith, storage.OpIStartsWith:
		pattern = "^" + regexp.QuoteMeta(s)
	case storage.OpEndsWith, storage.OpIEndsWith:
		pattern = regexp.QuoteMeta(s) + `\z`
	case storage.OpLike, storage.OpILike:
		pattern = likePattern(s)
		options = "s" // let % and _ match newlines too
	default:
		return nil, fmt.Errorf("unsupported operator %s", c.Op)
	}
	switch c.Op {
	case storage.OpIContains, storage.OpIStartsWith, storage.OpIEndsWith, storage.OpILike:
		options += "i"
	}
	return with("$regex", bson.Regex{Pattern: pattern, Options: options}), nil
}

// likePattern converts a SQL LIKE pattern into an anchored regex: % matches
// any run of characters, _ exactly one; everything else is literal.
func likePattern(s string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range s {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}

// buildSort translates sort fields; an empty list sorts by _id.
func buildSort(fields []storage.SortField) bson.D {
	if len(fields) == 0 {
		return bson.D{{Key: "_id", Value: 1}}
	}
	out := make(bson.D, len(fields))
	for i, f := range fields {
		dir := 1
		if f.Desc {
			dir = -1
		}
		out[i] = bson.E{Key: mongoField(f.Field), Value: dir}
	}
	return out
}
