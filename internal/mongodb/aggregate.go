package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Aggregator = (*Repository)(nil)

// bucketFormats are the $dateToString formats of the date buckets: ISO 8601 week-years for
// weeks, so the week of 2026-12-31 may be 2027-W01.
var bucketFormats = map[string]string{
	storage.BucketDay:   "%Y-%m-%d",
	storage.BucketWeek:  "%G-W%V",
	storage.BucketMonth: "%Y-%m",
	storage.BucketYear:  "%Y",
}

// Aggregate summarizes the matching documents with a fixed pipeline: $match (where and the
// read rule), $group, $sort and a $facet that counts the groups and limits them. Nothing a
// client sent becomes a stage, a field name or an operator: fields come from the declaration
// and everything else from this function.
func (r *Repository) Aggregate(ctx context.Context, q storage.AggregateQuery) (storage.AggregateResult, error) {
	var err error
	if q.Filter, err = r.blindFilter(ctx, q.Filter); err != nil {
		return storage.AggregateResult{}, err
	}
	if q.Access, err = r.blindFilter(ctx, q.Access); err != nil {
		return storage.AggregateResult{}, err
	}
	match, err := buildQuery(storage.Query{Filter: q.Filter, Access: q.Access, Search: q.Search})
	if err != nil {
		return storage.AggregateResult{}, err
	}

	id := bson.D{}
	for i, g := range q.GroupBy {
		path := "$" + mongoField(g.Field)
		var expr any = bson.D{{Key: "$ifNull", Value: bson.A{path, nil}}}
		if g.Bucket != "" {
			format, ok := bucketFormats[g.Bucket]
			if !ok {
				return storage.AggregateResult{}, fmt.Errorf("unknown bucket %q", g.Bucket)
			}
			d := bson.D{{Key: "format", Value: format}, {Key: "date", Value: path}}
			if g.TZ != "" {
				d = append(d, bson.E{Key: "timezone", Value: g.TZ})
			}
			expr = bson.D{{Key: "$dateToString", Value: d}}
		}
		id = append(id, bson.E{Key: fmt.Sprintf("k%d", i), Value: expr})
	}
	var groupID any = id
	if len(id) == 0 {
		groupID = nil
	}
	group := bson.D{{Key: "_id", Value: groupID}}
	var sizes bson.D
	for i, m := range q.Metrics {
		name := fmt.Sprintf("m%d", i)
		path := "$" + mongoField(m.Field)
		var acc bson.D
		switch m.Op {
		case storage.MetricCount:
			acc = bson.D{{Key: "$sum", Value: 1}}
		case storage.MetricCountDistinct:
			// Null and missing values aren't values; the set is cut after the cap so it
			// can be told from one that fits.
			acc = bson.D{{Key: "$addToSet", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{path, nil}}}, nil}}}, "$$REMOVE", path}}}}}
			cap := q.MaxDistinct + 1
			if q.MaxDistinct <= 0 {
				cap = 1 << 30
			}
			sizes = append(sizes, bson.E{Key: name, Value: bson.D{{Key: "$size", Value: bson.D{{Key: "$slice", Value: bson.A{"$" + name, cap}}}}}})
		case storage.MetricSum:
			acc = bson.D{{Key: "$sum", Value: bson.D{{Key: "$toDecimal", Value: path}}}}
		case storage.MetricAvg:
			acc = bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDecimal", Value: path}}}}
		case storage.MetricMin:
			acc = bson.D{{Key: "$min", Value: path}}
		case storage.MetricMax:
			acc = bson.D{{Key: "$max", Value: path}}
		default:
			return storage.AggregateResult{}, fmt.Errorf("unknown metric %q", m.Op)
		}
		group = append(group, bson.E{Key: name, Value: acc})
	}

	// Order: the requested names, then every key in order, so the answer is deterministic.
	field := func(name string) string {
		for i, g := range q.GroupBy {
			if g.Name() == name {
				return fmt.Sprintf("_id.k%d", i)
			}
		}
		for i, m := range q.Metrics {
			if m.Name() == name {
				return fmt.Sprintf("m%d", i)
			}
		}
		return ""
	}
	sort := bson.D{}
	used := map[string]bool{}
	for _, s := range q.Sort {
		f := field(s.Name)
		if f == "" || used[f] {
			continue
		}
		used[f] = true
		dir := 1
		if s.Desc {
			dir = -1
		}
		sort = append(sort, bson.E{Key: f, Value: dir})
	}
	for i := range q.GroupBy {
		f := fmt.Sprintf("_id.k%d", i)
		if !used[f] {
			sort = append(sort, bson.E{Key: f, Value: 1})
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	pipeline := bson.A{bson.D{{Key: "$match", Value: match}}, bson.D{{Key: "$group", Value: group}}}
	if len(sizes) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$set", Value: sizes}})
	}
	if len(sort) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$sort", Value: sort}})
	}
	pipeline = append(pipeline, bson.D{{Key: "$facet", Value: bson.D{
		{Key: "total", Value: bson.A{bson.D{{Key: "$count", Value: "n"}}}},
		{Key: "groups", Value: bson.A{bson.D{{Key: "$limit", Value: limit}}}},
	}}})

	runCtx := ctx
	if q.MaxTime > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, q.MaxTime)
		defer cancel()
	}
	cur, err := r.coll.Aggregate(runCtx, pipeline, options.Aggregate().SetAllowDiskUse(false))
	if err != nil {
		return storage.AggregateResult{}, aggregateError(ctx, runCtx, err)
	}
	var out []struct {
		Total []struct {
			N int64 `bson:"n"`
		} `bson:"total"`
		Groups []bson.D `bson:"groups"`
	}
	if err := cur.All(runCtx, &out); err != nil {
		return storage.AggregateResult{}, aggregateError(ctx, runCtx, err)
	}
	res := storage.AggregateResult{}
	if len(q.GroupBy) == 0 && (len(out) == 0 || len(out[0].Groups) == 0) {
		// A total over nothing is still one group (a counter says 0): the reference knows its values.
		return storage.AggregateDocs(nil, storage.AggregateQuery{Metrics: q.Metrics})
	}
	if len(out) == 0 {
		return res, nil
	}
	if len(out[0].Total) > 0 {
		res.TotalGroups = int(out[0].Total[0].N)
	}
	for _, d := range out[0].Groups {
		g := storage.AggregateGroup{Key: make([]any, len(q.GroupBy)), Values: map[string]any{}}
		for _, e := range d {
			switch {
			case e.Key == "_id":
				if kd, ok := e.Value.(bson.D); ok {
					for _, k := range kd {
						var i int
						if _, err := fmt.Sscanf(k.Key, "k%d", &i); err == nil && i < len(g.Key) {
							g.Key[i] = fromBSONValue(k.Value)
						}
					}
				}
			case len(e.Key) > 1 && e.Key[0] == 'm':
				var i int
				if _, err := fmt.Sscanf(e.Key, "m%d", &i); err != nil || i >= len(q.Metrics) {
					continue
				}
				m := q.Metrics[i]
				v := aggregateValue(m, e.Value)
				if m.Op == storage.MetricCountDistinct && q.MaxDistinct > 0 {
					if n, _ := v.(int64); n > int64(q.MaxDistinct) {
						return storage.AggregateResult{}, storage.ErrAggregateTooExpensive
					}
				}
				g.Values[m.Name()] = v
			}
		}
		res.Groups = append(res.Groups, g)
	}
	return res, nil
}

// aggregateValue converts what the database returned for a metric.
func aggregateValue(m storage.Metric, v any) any {
	switch m.Op {
	case storage.MetricCount, storage.MetricCountDistinct:
		switch n := v.(type) {
		case int32:
			return int64(n)
		case int64:
			return n
		}
	case storage.MetricSum, storage.MetricAvg:
		switch n := v.(type) {
		case bson.Decimal128:
			return storage.NewDecimal(n.String())
		case int32, int64, float64:
			return storage.NewDecimal(fmt.Sprint(n))
		case nil:
			return nil
		}
	}
	return fromBSONValue(v)
}

// aggregateError maps a failed aggregation: time, memory and the like are the caller's cost
// to reduce, not an outage.
func aggregateError(parent, run context.Context, err error) error {
	if run.Err() != nil && parent.Err() == nil {
		return storage.ErrAggregateTooExpensive
	}
	if ce, ok := errors.AsType[mongo.CommandError](err); ok {
		if ce.Code == 27 {
			return storage.ErrIndexBuilding
		}
		switch ce.Code {
		case 50, 292, 16819, 16820, 16945, 4568: // time, memory and the group limits
			return storage.ErrAggregateTooExpensive
		}
	}
	if mongo.IsTimeout(err) && parent.Err() == nil {
		return storage.ErrAggregateTooExpensive
	}
	return mapError(err)
}
