package query

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

// MaxAggregateMetrics is how many metrics one aggregation may ask for.
const MaxAggregateMetrics = 10

// DefaultAggregateLimit is the number of groups returned when `limit` isn't given.
const DefaultAggregateLimit = 100

// AggregateFields tells ParseAggregate what a collection declared in `aggregate:`.
type AggregateFields struct {
	GroupBy []string // declared group_by fields
	Metrics []string // declared metrics fields
	// Date says whether a field is stored as a date, and so can be bucketed (and measured by
	// min and max only); Numeric whether it holds numbers (sum and avg).
	Date    func(field string) bool
	Numeric func(field string) bool
	// MaxGroups is the instance's and collection's limit on groups.
	MaxGroups int
}

// AggregateParams are the raw parameters of an aggregate request.
type AggregateParams struct {
	GroupBy, Metrics, OrderBy, TZ, Limit string
}

// ParseAggregate reads and checks the parameters of an aggregation against the collection's
// declaration. Every problem found is returned, with the parameter it is about.
func ParseAggregate(p AggregateParams, f AggregateFields) (q storage.AggregateQuery, err error) {
	var errs Errors
	fail := func(param, format string, args ...any) {
		errs = append(errs, Error{Path: param, Reason: fmt.Sprintf(format, args...)})
	}

	tz := ""
	if p.TZ != "" {
		if _, e := time.LoadLocation(p.TZ); e != nil || p.TZ == "Local" {
			fail("tz", "%q is not an IANA time zone, like Europe/Madrid or UTC", p.TZ)
		} else {
			tz = p.TZ
		}
	}

	seen := map[string]bool{}
	if p.GroupBy != "" {
		for part := range strings.SplitSeq(p.GroupBy, ",") {
			field, bucket, hasBucket := strings.Cut(strings.TrimSpace(part), ":")
			switch {
			case !slices.Contains(f.GroupBy, field):
				fail("group_by", "%q is not a field this collection declares in aggregate.group_by%s", field, declared(f.GroupBy))
				continue
			case hasBucket && !slices.Contains([]string{storage.BucketDay, storage.BucketWeek, storage.BucketMonth, storage.BucketYear}, bucket):
				fail("group_by", "%q is not a bucket: use day, week, month or year", bucket)
				continue
			case hasBucket && !f.Date(field):
				fail("group_by", "%q is not a date: only dates can be bucketed", field)
				continue
			}
			g := storage.GroupField{Field: field, Bucket: bucket, TZ: tz}
			if seen[g.Name()] {
				fail("group_by", "%q is listed twice", g.Name())
				continue
			}
			seen[g.Name()] = true
			q.GroupBy = append(q.GroupBy, g)
		}
		if len(q.GroupBy) > 3 {
			fail("group_by", "%d fields, at most 3", len(q.GroupBy))
		}
	}

	metrics := p.Metrics
	if metrics == "" {
		metrics = storage.MetricCount
	}
	names := map[string]bool{}
	parts := strings.Split(metrics, ",")
	if len(parts) > MaxAggregateMetrics {
		fail("metrics", "%d metrics, at most %d", len(parts), MaxAggregateMetrics)
		parts = parts[:MaxAggregateMetrics]
	}
	for _, part := range parts {
		op, field, hasField := strings.Cut(strings.TrimSpace(part), ":")
		m := storage.Metric{Op: op, Field: field}
		switch op {
		case storage.MetricCount:
			if hasField {
				fail("metrics", "count takes no field")
				continue
			}
		case storage.MetricCountDistinct:
			if !hasField || !slices.Contains(f.GroupBy, field) {
				fail("metrics", "count_distinct:<field> needs a field of aggregate.group_by%s", declared(f.GroupBy))
				continue
			}
		case storage.MetricSum, storage.MetricAvg, storage.MetricMin, storage.MetricMax:
			switch {
			case !hasField || !slices.Contains(f.Metrics, field):
				fail("metrics", "%s:<field> needs a field of aggregate.metrics%s", op, declared(f.Metrics))
				continue
			case (op == storage.MetricSum || op == storage.MetricAvg) && !f.Numeric(field):
				fail("metrics", "%s:%s: only numbers can be summed or averaged (dates have min and max)", op, field)
				continue
			}
		default:
			fail("metrics", "%q is not a metric: use count, count_distinct:<field>, sum:<field>, avg:<field>, min:<field> or max:<field>", op)
			continue
		}
		if names[m.Name()] {
			fail("metrics", "%q is listed twice", part)
			continue
		}
		names[m.Name()] = true
		q.Metrics = append(q.Metrics, m)
	}

	if p.OrderBy != "" {
		known := map[string]bool{}
		for _, g := range q.GroupBy {
			known[g.Name()] = true
		}
		for n := range names {
			known[n] = true
		}
		for part := range strings.SplitSeq(p.OrderBy, ",") {
			name := strings.TrimSpace(part)
			desc := strings.HasPrefix(name, "-")
			name = strings.TrimPrefix(name, "-")
			if !known[name] {
				list := make([]string, 0, len(known))
				for k := range known {
					list = append(list, k)
				}
				slices.Sort(list)
				fail("order_by", "%q is not in this request's group_by or metrics; sort by one of %s", name, strings.Join(list, ", "))
				continue
			}
			q.Sort = append(q.Sort, storage.AggregateSort{Name: name, Desc: desc})
		}
	}

	q.Limit = min(DefaultAggregateLimit, f.MaxGroups)
	if p.Limit != "" {
		n, e := strconv.Atoi(p.Limit)
		if e != nil || n < 1 || n > f.MaxGroups {
			fail("limit", "must be an integer between 1 and %d", f.MaxGroups)
		} else {
			q.Limit = n
		}
	}

	if len(errs) > 0 {
		return storage.AggregateQuery{}, errs
	}
	return q, nil
}

func declared(list []string) string {
	if len(list) == 0 {
		return " (it declares none)"
	}
	return "; declared: " + strings.Join(list, ", ")
}
