package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Metric operations of an aggregation.
const (
	MetricCount         = "count"
	MetricCountDistinct = "count_distinct"
	MetricSum           = "sum"
	MetricAvg           = "avg"
	MetricMin           = "min"
	MetricMax           = "max"
)

// Date buckets of a group-by field.
const (
	BucketDay   = "day"
	BucketWeek  = "week" // ISO 8601, Monday first
	BucketMonth = "month"
	BucketYear  = "year"
)

// GroupField is one field an aggregation groups by, optionally bucketed (dates only).
type GroupField struct {
	Field  string
	Bucket string // "", day, week, month or year
	TZ     string // IANA zone of the buckets; "" is UTC
}

// Name is the key of the field in the groups' keys: the field, or field:bucket.
func (g GroupField) Name() string {
	if g.Bucket == "" {
		return g.Field
	}
	return g.Field + ":" + g.Bucket
}

// Metric is one measure of an aggregation.
type Metric struct {
	Op    string
	Field string // "" for count
}

// Name is the metric's name in a group: count, or <op>_<field>.
func (m Metric) Name() string {
	if m.Field == "" {
		return m.Op
	}
	return m.Op + "_" + m.Field
}

// AggregateSort orders the groups by a name of the key (Name of a GroupField) or of a metric.
type AggregateSort struct {
	Name string
	Desc bool
}

// AggregateQuery summarizes the documents that match Filter and Access.
type AggregateQuery struct {
	Filter  Filter
	Access  Filter
	Search  *Search // restricts the documents like a list's search
	GroupBy []GroupField
	Metrics []Metric
	// Sort orders the groups; the keys in order break ties, so the order is total.
	Sort  []AggregateSort
	Limit int
	// MaxTime bounds the work; 0 means no limit. MaxDistinct is the most distinct values
	// count_distinct may count (0: no limit).
	MaxTime     time.Duration
	MaxDistinct int
}

// AggregateGroup is one group of a result: Key has one value per GroupBy field (nil for the
// missing or null group) and Values one value per metric, by metric name.
type AggregateGroup struct {
	Key    []any
	Values map[string]any
}

// AggregateResult is the answer: the groups after sorting and limiting, and how many there
// were before the limit.
type AggregateResult struct {
	Groups      []AggregateGroup
	TotalGroups int
}

// Decimal is an exact decimal number as text: what sum and avg return, so that no precision is
// lost to floating point. It marshals as a JSON string.
type Decimal string

// NewDecimal normalizes the text of a decimal: no trailing zeros after the point, and no point
// when nothing is left of the fraction ("300.50" is "300.5", "3.0" is "3"). The database keeps
// the digits of the values it was given; the answer is the number, not its history.
func NewDecimal(s string) Decimal {
	if strings.ContainsAny(s, "eE") || !strings.Contains(s, ".") {
		return Decimal(s)
	}
	s = strings.TrimRight(s, "0")
	return Decimal(strings.TrimSuffix(s, "."))
}

// ErrAggregateTooExpensive means the aggregation needed more time, memory or distinct values
// than allowed.
var ErrAggregateTooExpensive = errors.New("aggregation too expensive")

// Aggregator is implemented by repositories that can summarize their documents. Sum and avg
// are returned as exact decimal strings, counts as int64, min and max as stored (numbers,
// times), and dates in keys as bucket text.
type Aggregator interface {
	Aggregate(ctx context.Context, q AggregateQuery) (AggregateResult, error)
}

// AggregateDocs is the reference implementation of an aggregation over documents held in
// memory: the same groups, keys, order and values the database gives, so tests can compare.
func AggregateDocs(docs []Document, q AggregateQuery) (AggregateResult, error) {
	type bucket struct {
		key    []any
		values []Document
	}
	groups := map[string]*bucket{}
	var order []string
	for _, d := range docs {
		for _, f := range []Filter{q.Filter, q.Access} {
			if f == nil {
				continue
			}
			ok, err := Match(d, f)
			if err != nil {
				return AggregateResult{}, err
			}
			if !ok {
				goto next
			}
		}
		if q.Search != nil {
			if _, ok := MatchSearch(d, q.Search.Text); !ok {
				continue
			}
		}
		{
			key := make([]any, len(q.GroupBy))
			var id strings.Builder
			for i, g := range q.GroupBy {
				key[i] = groupValue(d, g)
				fmt.Fprintf(&id, "%T:%v\x00", key[i], key[i])
			}
			b := groups[id.String()]
			if b == nil {
				b = &bucket{key: key}
				groups[id.String()] = b
				order = append(order, id.String())
			}
			b.values = append(b.values, d)
		}
	next:
	}
	if len(q.GroupBy) == 0 && len(groups) == 0 {
		// A total is always one group, even over nothing: a counter says 0.
		groups[""] = &bucket{key: []any{}}
		order = append(order, "")
	}
	res := AggregateResult{TotalGroups: len(groups)}
	for _, id := range order {
		b := groups[id]
		g := AggregateGroup{Key: b.key, Values: map[string]any{}}
		for _, m := range q.Metrics {
			v, err := metricValue(b.values, m, q.MaxDistinct)
			if err != nil {
				return AggregateResult{}, err
			}
			g.Values[m.Name()] = v
		}
		res.Groups = append(res.Groups, g)
	}
	sortGroups(res.Groups, q)
	if q.Limit > 0 && len(res.Groups) > q.Limit {
		res.Groups = res.Groups[:q.Limit]
	}
	return res, nil
}

// groupValue is a document's value for one group field: a bucket's text, the value itself or nil.
func groupValue(d Document, g GroupField) any {
	v := lookup(d, g.Field)
	if g.Bucket == "" {
		return v
	}
	t, ok := v.(time.Time)
	if !ok {
		return nil
	}
	loc := time.UTC
	if g.TZ != "" {
		if l, err := time.LoadLocation(g.TZ); err == nil {
			loc = l
		}
	}
	t = t.In(loc)
	switch g.Bucket {
	case BucketDay:
		return t.Format("2006-01-02")
	case BucketWeek:
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case BucketMonth:
		return t.Format("2006-01")
	case BucketYear:
		return t.Format("2006")
	}
	return nil
}

func metricValue(docs []Document, m Metric, maxDistinct int) (any, error) {
	switch m.Op {
	case MetricCount:
		return int64(len(docs)), nil
	case MetricCountDistinct:
		seen := map[string]bool{}
		for _, d := range docs {
			if v := lookup(d, m.Field); v != nil {
				seen[fmt.Sprintf("%T:%v", v, v)] = true
			}
		}
		if maxDistinct > 0 && len(seen) > maxDistinct {
			return nil, ErrAggregateTooExpensive
		}
		return int64(len(seen)), nil
	case MetricSum, MetricAvg:
		sum, scale, n := new(big.Rat), 0, 0
		for _, d := range docs {
			r, s, ok := decimalOf(lookup(d, m.Field))
			if !ok {
				continue
			}
			sum.Add(sum, r)
			scale = max(scale, s)
			n++
		}
		if m.Op == MetricSum {
			return NewDecimal(formatDecimal(sum, scale)), nil
		}
		if n == 0 {
			return nil, nil
		}
		return NewDecimal(formatQuotient(sum, int64(n), scale)), nil
	case MetricMin, MetricMax:
		var best any
		for _, d := range docs {
			v := lookup(d, m.Field)
			if v == nil {
				continue
			}
			if best == nil {
				best = v
				continue
			}
			c := compareAggregate(v, best)
			if (m.Op == MetricMin && c < 0) || (m.Op == MetricMax && c > 0) {
				best = v
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("unknown metric %q", m.Op)
}

// decimalOf reads a number as an exact decimal, and how many fraction digits its shortest
// text form has.
func decimalOf(v any) (r *big.Rat, scale int, ok bool) {
	var s string
	switch n := v.(type) {
	case int64:
		s = strconv.FormatInt(n, 10)
	case int:
		s = strconv.Itoa(n)
	case float64:
		s = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return nil, 0, false
	}
	r, ok = new(big.Rat).SetString(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		scale = len(s) - i - 1
	}
	return r, scale, ok
}

func formatDecimal(r *big.Rat, scale int) string {
	return r.FloatString(scale)
}

// formatQuotient is sum/n to at most 34 significant digits, as the database's decimal type
// gives it: exact when it fits, rounded half to even otherwise, without trailing zeros past
// the inputs' scale.
func formatQuotient(sum *big.Rat, n int64, scale int) string {
	q := new(big.Rat).Quo(sum, big.NewRat(n, 1))
	for s := scale; s <= 40; s++ {
		if new(big.Rat).Mul(q, new(big.Rat).SetInt(pow10(s))).IsInt() {
			if digits(q, s) <= 34 {
				return q.FloatString(s)
			}
			break
		}
	}
	// Not exact in 34 digits: round to 34 significant digits.
	intDigits := len(new(big.Int).Abs(new(big.Int).Quo(q.Num(), q.Denom())).String())
	if new(big.Int).Quo(q.Num(), q.Denom()).Sign() == 0 {
		intDigits = 0
	}
	places := 34 - intDigits
	if intDigits == 0 {
		// leading fractional zeros don't count as significant
		f := new(big.Rat).Abs(q)
		z := 0
		ten := big.NewRat(10, 1)
		for f.Sign() != 0 && f.Cmp(big.NewRat(1, 10)) < 0 {
			f.Mul(f, ten)
			z++
		}
		places = 34 + z
	}
	return roundHalfEven(q, places)
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// digits counts the significant digits of q written with s fraction digits.
func digits(q *big.Rat, s int) int {
	scaled := new(big.Int).Mul(q.Num(), pow10(s))
	scaled.Quo(scaled, q.Denom())
	return len(new(big.Int).Abs(scaled).String())
}

func roundHalfEven(q *big.Rat, places int) string {
	scale := new(big.Int).Set(pow10(places))
	num := new(big.Int).Mul(q.Num(), scale)
	quo, rem := new(big.Int).QuoRem(num, q.Denom(), new(big.Int))
	twice := new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2))
	switch c := twice.Cmp(q.Denom()); {
	case c > 0, c == 0 && quo.Bit(0) == 1:
		if q.Sign() < 0 {
			quo.Sub(quo, big.NewInt(1))
		} else {
			quo.Add(quo, big.NewInt(1))
		}
	}
	return new(big.Rat).SetFrac(quo, scale).FloatString(places)
}

// sortGroups orders groups by q.Sort, then by the keys in order, nil first.
func sortGroups(gs []AggregateGroup, q AggregateQuery) {
	keyIndex := map[string]int{}
	for i, g := range q.GroupBy {
		keyIndex[g.Name()] = i
	}
	value := func(g AggregateGroup, name string) any {
		if i, ok := keyIndex[name]; ok {
			return g.Key[i]
		}
		return g.Values[name]
	}
	slices.SortStableFunc(gs, func(a, b AggregateGroup) int {
		for _, s := range q.Sort {
			c := compareAggregate(value(a, s.Name), value(b, s.Name))
			if s.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		for i := range q.GroupBy {
			if c := compareAggregate(a.Key[i], b.Key[i]); c != 0 {
				return c
			}
		}
		return 0
	})
}

// compareAggregate orders values as the database does across the kinds a group can hold: null,
// numbers (decimal strings as numbers), strings, booleans, times.
func compareAggregate(a, b any) int {
	ra, rb := kindRank(a), kindRank(b)
	if ra != rb {
		return cmp.Compare(ra, rb)
	}
	switch x := a.(type) {
	case nil:
		return 0
	case string:
		return strings.Compare(x, b.(string))

	case bool:
		return cmp.Compare(b2i(x), b2i(b.(bool)))
	case time.Time:
		return x.Compare(b.(time.Time))
	}
	return compareNumbers(a, b)
}

// compareNumbers compares two numbers (int64, float64 or Decimal) by value.
func compareNumbers(a, b any) int {
	ra, rb := ratOf(a), ratOf(b)
	if ra == nil || rb == nil {
		return 0
	}
	return ra.Cmp(rb)
}

func ratOf(v any) *big.Rat {
	if d, ok := v.(Decimal); ok {
		r, _ := new(big.Rat).SetString(string(d))
		return r
	}
	r, _, _ := decimalOf(v)
	return r
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// kindRank is the order of kinds in a sort: null, numbers, strings, booleans, times. Decimal
// strings from sum and avg rank as numbers: callers compare them only with each other.
func kindRank(v any) int {
	switch v.(type) {
	case nil:
		return 0
	case int64, int, float64, Decimal:
		return 1
	case string:
		return 2
	case bool:
		return 3
	case time.Time:
		return 4
	}
	return 5
}
