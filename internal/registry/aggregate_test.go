package registry

import (
	"strings"
	"testing"
	"time"
)

const salesSchema = `{
  "type": "object",
  "properties": {
    "status":   {"enum": ["new", "paid"]},
    "category": {"type": "string"},
    "paid":     {"type": "boolean"},
    "total":    {"type": "number"},
    "qty":      {"type": "integer"},
    "paid_at":  {"type": "string", "format": "date-time", "x-backd-store": "date"},
    "note":     {"type": ["string", "null"]},
    "tags":     {"type": "array", "items": {"type": "string"}},
    "address":  {"type": "object", "properties": {"city": {"type": "string"}}},
    "lines":    {"type": "array", "items": {"type": "object", "properties": {"sku": {"type": "string"}}}},
    "blob":     {"type": "object"}
  }
}`

func salesTree(t *testing.T, yaml string, indexes string) (*Registry, error) {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":                 "signup: open\norganizations:\n  roles:\n    owner: {manage: [org, members, roles, teams]}\n    member: {}\n",
		"shop/main/sales/schema.json":     salesSchema,
		"shop/main/sales/collection.yaml": yaml,
	}
	if indexes != "" {
		files["shop/main/sales/indexes.json"] = indexes
	}
	return Load(writeTree(t, files))
}

func TestAggregateDeclared(t *testing.T) {
	reg, err := salesTree(t, `organization: required
on_org_delete: {action: delete}
aggregate:
  group_by: [status, category, paid, qty, paid_at, address.city, _meta.org, _meta.owner, _meta.created_at]
  metrics: [total, qty, paid_at, _meta.created_at]
  limits:
    time: 2s
    max_groups: 300
`, "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "sales")
	a := c.Aggregate
	if a == nil || len(a.GroupBy) != 9 || len(a.Metrics) != 4 || a.Time != 2*time.Second || a.MaxGroups != 300 {
		t.Fatalf("Aggregate = %+v", a)
	}
	if got := a.EffectiveTime(DefaultAggregateLimits); got != 2*time.Second {
		t.Errorf("EffectiveTime = %s", got)
	}
	if got := a.EffectiveMaxGroups(AggregateLimits{Time: time.Second, MaxGroups: 100}); got != 100 {
		t.Errorf("a collection never raises the instance's limit: %d", got)
	}
	if other, err := salesTree(t, "rules:\n  read: true\n", ""); err != nil {
		t.Fatal(err)
	} else if c, _ := other.Collection("shop", "main", "sales"); c.Aggregate != nil {
		t.Errorf("a collection without aggregate: has %+v", c.Aggregate)
	}
}

func TestAggregateErrors(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"empty":           {"aggregate: {}\n", "group_by and metrics are both empty"},
		"unknown field":   {"aggregate:\n  group_by: [nope]\n", `group_by: field "nope" is not declared`},
		"an array":        {"aggregate:\n  group_by: [tags]\n", `is a array`},
		"in an array":     {"aggregate:\n  group_by: [lines.sku]\n", `is inside an array`},
		"an object":       {"aggregate:\n  group_by: [blob]\n", `is a object`},
		"id":              {"aggregate:\n  group_by: [id]\n", `can't be grouped by`},
		"no org":          {"aggregate:\n  group_by: [_meta.org]\n", "this collection has no org"},
		"twice":           {"aggregate:\n  group_by: [status, status]\n", `"status" is listed twice`},
		"metric a string": {"aggregate:\n  metrics: [category]\n", `field "category" is a string`},
		"metric an enum":  {"aggregate:\n  metrics: [status]\n", `has no numeric type`},
		"metric unknown":  {"aggregate:\n  metrics: [nope]\n", `metrics: field "nope" is not declared`},
		"metric meta":     {"aggregate:\n  metrics: [_meta.owner]\n", `can't be measured`},
		"bad time":        {"aggregate:\n  group_by: [status]\n  limits: {time: soon}\n", "is not a duration"},
		"short time":      {"aggregate:\n  group_by: [status]\n  limits: {time: 1ms}\n", "is too short"},
		"zero groups":     {"aggregate:\n  group_by: [status]\n  limits: {max_groups: 0}\n", "must be at least 1"},
		"unknown key":     {"aggregate:\n  group_by: [status]\n  order: x\n", "field order not found"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := salesTree(t, tc.yaml, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "sales/collection.yaml") {
				t.Errorf("the error doesn't name the file: %v", err)
			}
		})
	}
}

func TestAggregateIndexWarnings(t *testing.T) {
	reg, err := salesTree(t, "aggregate:\n  group_by: [status, category, _meta.created_at, paid_at]\n  metrics: [total]\n", `[{"fields": ["status", "-paid_at"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "sales")
	w := c.AggregateIndexWarnings()
	// status leads an index and _meta.created_at the automatic one; category and paid_at don't.
	if len(w) != 2 || !strings.Contains(w[0], `"category"`) || !strings.Contains(w[1], `"paid_at"`) || !strings.Contains(w[0], `{"fields": ["category"]}`) {
		t.Fatalf("warnings = %q", w)
	}
	if got := reg.AggregateWarnings(); len(got) != 2 {
		t.Errorf("registry warnings = %q", got)
	}
}

func TestCheckAggregateLimits(t *testing.T) {
	reg, err := salesTree(t, "aggregate:\n  group_by: [status]\n  limits: {time: 4s, max_groups: 500}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckAggregateLimits(DefaultAggregateLimits); err != nil {
		t.Fatal(err)
	}
	err = reg.CheckAggregateLimits(AggregateLimits{Time: 3 * time.Second, MaxGroups: 100})
	if err == nil || !strings.Contains(err.Error(), "BACKD_AGGREGATE_TIME") || !strings.Contains(err.Error(), "BACKD_AGGREGATE_MAX_GROUPS") {
		t.Fatalf("err = %v", err)
	}
}
