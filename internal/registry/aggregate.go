package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// AggregateLimits are what an aggregation may cost on this instance; a collection may only
// lower them (collection.yaml's aggregate.limits).
type AggregateLimits struct {
	Time      time.Duration // the most time one aggregation may run
	MaxGroups int           // the most groups one aggregation may return
}

// DefaultAggregateLimits are the limits when the instance sets none.
var DefaultAggregateLimits = AggregateLimits{Time: 5 * time.Second, MaxGroups: 1000}

// OrDefault fills the limits that are zero with the defaults.
func (l AggregateLimits) OrDefault() AggregateLimits {
	if l.Time <= 0 {
		l.Time = DefaultAggregateLimits.Time
	}
	if l.MaxGroups <= 0 {
		l.MaxGroups = DefaultAggregateLimits.MaxGroups
	}
	return l
}

// MaxAggregateGroupBy is how many fields one aggregation may group by.
const MaxAggregateGroupBy = 3

// minAggregateTime is the shortest time limit a collection may set: below it nothing real runs.
const minAggregateTime = 100 * time.Millisecond

// Aggregate is collection.yaml's `aggregate:`: which fields of the collection can be grouped
// and measured by the aggregate endpoint. A collection without it can't be aggregated.
type Aggregate struct {
	GroupBy []string // fields a request may group by, in declaration order
	Metrics []string // numeric (and date) fields a request may sum, average, min or max
	// Time and MaxGroups lower the instance's limits for this collection; 0 uses the instance's.
	Time      time.Duration
	MaxGroups int
}

// AggregateGroupFields are the system fields a collection may group by (date ones can be
// bucketed). `_meta.org` and `_meta.team` only exist in collections that have them.
var aggregateGroupSystem = []string{"_meta.owner", "_meta.created_by", "_meta.updated_by", "_meta.created_at", "_meta.updated_at"}

type aggregateDoc struct {
	GroupBy []string `yaml:"group_by"`
	Metrics []string `yaml:"metrics"`
	Limits  *struct {
		Time      string `yaml:"time"`
		MaxGroups *int   `yaml:"max_groups"`
	} `yaml:"limits"`
}

// loadAggregate reads collection.yaml's aggregate: into c and checks it against the schema.
// It runs after the indexes and the organization policy, whose fields it may name.
func loadAggregate(c *Collection, path string, data []byte) error {
	if data == nil {
		return nil
	}
	var doc struct {
		Aggregate *aggregateDoc `yaml:"aggregate"`
	}
	if ok, err := decodeSection(data, path, &doc, "aggregate", "group_by", "max_groups"); !ok || err != nil {
		return err
	}
	if doc.Aggregate == nil {
		return nil
	}
	d := doc.Aggregate
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: aggregate."+format, append([]any{path}, args...)...))
	}
	a := &Aggregate{}
	if len(d.GroupBy) == 0 && len(d.Metrics) == 0 {
		fail("group_by and metrics are both empty: declare the fields a request may group by and measure (counts need none, but the section must name something)")
	}
	seen := map[string]bool{}
	for _, f := range d.GroupBy {
		if seen[f] {
			fail("group_by: %q is listed twice", f)
			continue
		}
		seen[f] = true
		if why := c.aggregateGroupProblem(f); why != "" {
			fail("group_by: %s", why)
			continue
		}
		a.GroupBy = append(a.GroupBy, f)
	}
	seen = map[string]bool{}
	for _, f := range d.Metrics {
		if seen[f] {
			fail("metrics: %q is listed twice", f)
			continue
		}
		seen[f] = true
		if why := c.aggregateMetricProblem(f); why != "" {
			fail("metrics: %s", why)
			continue
		}
		a.Metrics = append(a.Metrics, f)
	}
	if l := d.Limits; l != nil {
		if l.Time != "" {
			t, err := time.ParseDuration(l.Time)
			switch {
			case err != nil:
				fail("limits.time: %q is not a duration like 3s or 500ms", l.Time)
			case t < minAggregateTime:
				fail("limits.time: %s is too short: at least %s", l.Time, minAggregateTime)
			default:
				a.Time = t
			}
		}
		if l.MaxGroups != nil {
			if *l.MaxGroups < 1 {
				fail("limits.max_groups: must be at least 1, got %d", *l.MaxGroups)
			} else {
				a.MaxGroups = *l.MaxGroups
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	c.Aggregate = a
	return nil
}

// aggregateGroupProblem says why f can't be grouped by, or "" when it can.
func (c *Collection) aggregateGroupProblem(f string) string {
	switch {
	case slices.Contains(aggregateGroupSystem, f):
		return ""
	case f == MetaOrg && c.InOrganizations(), f == MetaTeam && c.InTeams():
		return ""
	case f == MetaOrg || f == MetaTeam:
		return fmt.Sprintf("%s: this collection has no %s (set `organization`%s)", f, strings.TrimPrefix(f, "_meta."), map[bool]string{true: " and `team`", false: ""}[f == MetaTeam])
	case strings.HasPrefix(f, "_meta.") || f == "id":
		return fmt.Sprintf("%q can't be grouped by: use %s", f, strings.Join(aggregateGroupSystem, ", "))
	}
	info, ok := c.Fields[f]
	if !ok {
		return fmt.Sprintf("field %q is not declared in schema.json", f)
	}
	if c.throughArray(f) {
		return fmt.Sprintf("field %q is inside an array: only fields that hold one value can be grouped by", f)
	}
	if info.Date {
		return ""
	}
	// A field with no type is an enum or a const: its values are scalars.
	for _, t := range info.Types {
		if !slices.Contains([]string{"string", "boolean", "integer", "number", "null"}, t) {
			return fmt.Sprintf("field %q is a %s: only strings, booleans, numbers and dates can be grouped by", f, t)
		}
	}
	return ""
}

// aggregateMetricProblem says why f can't be measured, or "" when it can.
func (c *Collection) aggregateMetricProblem(f string) string {
	switch f {
	case "_meta.created_at", "_meta.updated_at", "_meta.version":
		return ""
	}
	if strings.HasPrefix(f, "_meta.") || f == "id" {
		return fmt.Sprintf("%q can't be measured: use a numeric or date field, _meta.created_at, _meta.updated_at or _meta.version", f)
	}
	info, ok := c.Fields[f]
	if !ok {
		return fmt.Sprintf("field %q is not declared in schema.json", f)
	}
	if c.throughArray(f) {
		return fmt.Sprintf("field %q is inside an array: only fields that hold one value can be measured", f)
	}
	if info.Date {
		return ""
	}
	numeric := false
	for _, t := range info.Types {
		switch t {
		case "integer", "number":
			numeric = true
		case "null":
		default:
			return fmt.Sprintf("field %q is a %s: only numbers (and dates, for min and max) can be measured", f, t)
		}
	}
	if !numeric {
		return fmt.Sprintf("field %q has no numeric type in schema.json: declare integer or number", f)
	}
	return ""
}

// EffectiveTime is the time limit of one aggregation of c: the collection's, never above the
// instance's.
func (a *Aggregate) EffectiveTime(instance AggregateLimits) time.Duration {
	if a.Time > 0 && a.Time < instance.Time {
		return a.Time
	}
	return instance.Time
}

// EffectiveMaxGroups is the group limit of one aggregation of c.
func (a *Aggregate) EffectiveMaxGroups(instance AggregateLimits) int {
	if a.MaxGroups > 0 && a.MaxGroups < instance.MaxGroups {
		return a.MaxGroups
	}
	return instance.MaxGroups
}

// CheckAggregateLimits fails for a collection whose aggregate.limits are above the instance's.
func (r *Registry) CheckAggregateLimits(instance AggregateLimits) error {
	var errs []error
	for _, db := range r.Databases() {
		for _, c := range db.SortedCollections() {
			a := c.Aggregate
			if a == nil {
				continue
			}
			if a.Time > instance.Time {
				errs = append(errs, fmt.Errorf("%s: aggregate.limits.time: %s is above this instance's limit of %s (BACKD_AGGREGATE_TIME); a collection can only lower it", c.ConfigPath(), a.Time, instance.Time))
			}
			if a.MaxGroups > instance.MaxGroups {
				errs = append(errs, fmt.Errorf("%s: aggregate.limits.max_groups: %d is above this instance's limit of %d (BACKD_AGGREGATE_MAX_GROUPS); a collection can only lower it", c.ConfigPath(), a.MaxGroups, instance.MaxGroups))
			}
		}
	}
	return errors.Join(errs...)
}

// AggregateIndexWarnings names the group_by fields that no index of the collection leads with,
// with the index to add: grouping on them scans every matching document, unsorted.
func (c *Collection) AggregateIndexWarnings() []string {
	if c.Aggregate == nil {
		return nil
	}
	led := map[string]bool{}
	for _, ix := range c.Indexes {
		if len(ix.Keys) > 0 {
			led[ix.Keys[0].Field] = true
		}
	}
	var out []string
	for _, f := range c.Aggregate.GroupBy {
		if led[f] {
			continue
		}
		out = append(out, fmt.Sprintf("%s/%s/%s: aggregate.group_by %q has no index that starts with it, so grouping by it scans every matching document: add {\"fields\": [%q]} to indexes.json", c.Realm, c.Database, c.Name, f, f))
	}
	return out
}

// AggregateWarnings are the index warnings of every collection of the registry.
func (r *Registry) AggregateWarnings() []string {
	var out []string
	for _, db := range r.Databases() {
		for _, c := range db.SortedCollections() {
			out = append(out, c.AggregateIndexWarnings()...)
		}
	}
	return out
}
