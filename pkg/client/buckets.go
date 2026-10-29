package client

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BucketUnit is the size of a date bucket of an aggregation.
type BucketUnit string

const (
	BucketDay   BucketUnit = "day"
	BucketWeek  BucketUnit = "week"
	BucketMonth BucketUnit = "month"
	BucketYear  BucketUnit = "year"
)

var bucketKeys = map[BucketUnit]*regexp.Regexp{
	BucketDay:   regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`),
	BucketWeek:  regexp.MustCompile(`^\d{4}-W\d{2}$`),
	BucketMonth: regexp.MustCompile(`^\d{4}-\d{2}$`),
	BucketYear:  regexp.MustCompile(`^\d{4}$`),
}

// BucketKey is the bucket key of a date (in UTC), in the text form the server uses: "2026-03-07",
// "2026-W10" (ISO 8601 weeks, from Monday), "2026-03" or "2026".
func BucketKey(t time.Time, unit BucketUnit) (string, error) {
	t = t.UTC()
	switch unit {
	case BucketDay:
		return t.Format("2006-01-02"), nil
	case BucketWeek:
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w), nil
	case BucketMonth:
		return t.Format("2006-01"), nil
	case BucketYear:
		return t.Format("2006"), nil
	}
	return "", fmt.Errorf("client: unknown bucket unit %q", unit)
}

// keyStart is the first instant (UTC) of the bucket a key names.
func keyStart(key string, unit BucketUnit) (time.Time, error) {
	switch unit {
	case BucketDay:
		return time.Parse("2006-01-02", key)
	case BucketMonth:
		return time.Parse("2006-01", key)
	case BucketYear:
		return time.Parse("2006", key)
	case BucketWeek:
		ys, ws, _ := strings.Cut(key, "-W")
		y, err1 := strconv.Atoi(ys)
		w, err2 := strconv.Atoi(ws)
		if err1 != nil || err2 != nil {
			return time.Time{}, fmt.Errorf("client: %q is not a week key", key)
		}
		jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC)
		wd := int(jan4.Weekday())
		if wd == 0 {
			wd = 7
		}
		monday := jan4.AddDate(0, 0, -(wd - 1))
		return monday.AddDate(0, 0, (w-1)*7), nil
	}
	return time.Time{}, fmt.Errorf("client: unknown bucket unit %q", unit)
}

func nextBucket(t time.Time, unit BucketUnit) time.Time {
	switch unit {
	case BucketDay:
		return t.AddDate(0, 0, 1)
	case BucketWeek:
		return t.AddDate(0, 0, 7)
	case BucketMonth:
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(1, 0, 0)
}

func toBucketKey(v any, unit BucketUnit, what string) (string, error) {
	switch x := v.(type) {
	case string:
		if bucketKeys[unit].MatchString(x) {
			return x, nil
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return BucketKey(t, unit)
		}
	case time.Time:
		return BucketKey(x, unit)
	}
	example, _ := BucketKey(time.Unix(0, 0), unit)
	return "", fmt.Errorf("client: %s is not a date or a %s key like %s", what, unit, example)
}

// FillBucketsOptions say which buckets to fill.
type FillBucketsOptions struct {
	// Field is the grouped date field, and Unit its bucket: the groups must be grouped by
	// "<Field>:<Unit>" and nothing else.
	Field string
	Unit  BucketUnit
	// From and To are bucket keys in the zone of the aggregation ("2026-01"), or time.Time values,
	// which name the bucket they fall in as seen in UTC; both are included.
	From, To any
	// Empty holds the values of an empty bucket's metrics (for sums, {"sum_total": "0"}); count is 0.
	Empty map[string]any
}

const maxFilledBuckets = 5000

// FillBuckets fills the buckets an aggregation left out, so a chart has a point for every day,
// week, month or year between From and To. The server doesn't return empty buckets; this adds
// them to groups grouped by one date field and nothing else (GroupBy "paid_at:month"). The groups
// come back in key order; a group with a null key (documents with no date) stays at the front.
func FillBuckets(groups []AggregateGroup, o FillBucketsOptions) ([]AggregateGroup, error) {
	re, ok := bucketKeys[o.Unit]
	if !ok {
		return nil, fmt.Errorf("client: unit must be day, week, month or year, got %q", o.Unit)
	}
	name := o.Field + ":" + string(o.Unit)
	byKey := map[string]AggregateGroup{}
	var null *AggregateGroup
	for i, g := range groups {
		if len(g.Key) != 1 {
			return nil, fmt.Errorf("client: FillBuckets needs groups grouped only by %s", name)
		}
		v, has := g.Key[name]
		if !has {
			return nil, fmt.Errorf("client: FillBuckets needs groups grouped only by %s", name)
		}
		if v == nil {
			null = &groups[i]
			continue
		}
		if s, isString := v.(string); isString && re.MatchString(s) {
			byKey[s] = g
		}
	}
	from, err := toBucketKey(o.From, o.Unit, "From")
	if err != nil {
		return nil, err
	}
	to, err := toBucketKey(o.To, o.Unit, "To")
	if err != nil {
		return nil, err
	}
	if from > to {
		return nil, fmt.Errorf("client: From (%s) is after To (%s)", from, to)
	}
	var out []AggregateGroup
	if null != nil {
		out = append(out, *null)
	}
	cursor, err := keyStart(from, o.Unit)
	if err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		key, _ := BucketKey(cursor, o.Unit)
		if key > to {
			break
		}
		if i >= maxFilledBuckets {
			return nil, fmt.Errorf("client: more than %d buckets between %s and %s: use a larger unit", maxFilledBuckets, from, to)
		}
		if g, ok := byKey[key]; ok {
			out = append(out, g)
		} else {
			values := map[string]any{"count": float64(0)}
			for k, v := range o.Empty {
				values[k] = v
			}
			out = append(out, AggregateGroup{Key: map[string]any{name: key}, Values: values})
		}
		cursor = nextBucket(cursor, o.Unit)
	}
	return out, nil
}
