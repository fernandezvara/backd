package registry

import (
	"strings"
	"testing"
)

const triggerOrdersSchema = `{
  "type": "object",
  "properties": {
    "status": {"enum": ["new", "paid", "shipped", "merged"]},
    "total":  {"type": "number"},
    "note":   {"type": "string"}
  }
}`

// triggerTree is a realm with one collection, orders, and an internal async function.
func triggerTree(t *testing.T, collectionYAML, fnYAML, realmYAML string) (*Registry, error) {
	t.Helper()
	if fnYAML == "" {
		fnYAML = "internal: true\nmode: async\n"
	}
	if realmYAML == "" {
		realmYAML = "auth: enabled\nsignup: open\n"
	}
	return Load(writeTree(t, map[string]string{
		"shop/realm.yaml":                            realmYAML,
		"shop/main/orders/schema.json":               triggerOrdersSchema,
		"shop/main/orders/collection.yaml":           collectionYAML,
		"shop/main/_functions/deno.json":             `{}`,
		"shop/main/_functions/on-paid/function.yaml": fnYAML,
		"shop/main/_functions/on-paid/index.ts":      "export default () => 1;\n",
	}))
}

func TestTriggersDeclared(t *testing.T) {
	reg, err := triggerTree(t, `triggers:
  on_paid:
    events: [updated, created]
    when: "document.status == 'paid' && before.status != 'paid'"
    function: main/on-paid
    fields: [status, total]
    session: true
  nightly_check:
    events: [removed]
    when: "before.total > 100"
    function: main/on-paid
    fields: ["*"]
    ordered: true
`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "orders")
	if len(c.Triggers) != 2 || !c.NeedsEvents() {
		t.Fatalf("Triggers = %+v", c.Triggers)
	}
	var paid, check *Trigger
	for _, tr := range c.Triggers {
		switch tr.Name {
		case "on_paid":
			paid = tr
		case "nightly_check":
			check = tr
		}
	}
	if paid == nil || check == nil {
		t.Fatalf("triggers = %+v", c.Triggers)
	}
	if strings.Join(paid.Events, ",") != "created,updated" || paid.When == nil || paid.Function != "main/on-paid" ||
		strings.Join(paid.Fields, ",") != "status,total" || !paid.Session || paid.Ordered || !paid.FiresOn("created") || paid.FiresOn("removed") {
		t.Errorf("on_paid = %+v", paid)
	}
	if !check.Ordered || len(check.Fields) != 1 || check.Fields[0] != "*" || check.Session {
		t.Errorf("nightly_check = %+v", check)
	}
	got := reg.EventCollections()
	if len(got) != 1 || got[0].Triggers != 2 || got[0].Realtime {
		t.Errorf("EventCollections = %+v", got)
	}
}

func TestTriggerChecks(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"no events":               {"triggers:\n  t:\n    function: main/on-paid\n", "triggers.t: events: list at least one of created, updated, removed"},
		"bad event":               {"triggers:\n  t:\n    events: [inserted]\n    function: main/on-paid\n", `events: "inserted" is not an event`},
		"duplicate event":         {"triggers:\n  t:\n    events: [created, created]\n    function: main/on-paid\n", `events: "created" is listed twice`},
		"no function":             {"triggers:\n  t:\n    events: [created]\n", "function is required"},
		"function not a path":     {"triggers:\n  t:\n    events: [created]\n    function: on-paid\n", `function "on-paid" must be <database>/<function>`},
		"unknown function":        {"triggers:\n  t:\n    events: [created]\n    function: main/nothing\n", `triggers.t.function: "main/nothing" is not a function of this realm`},
		"bad name":                {"triggers:\n  Bad_Name:\n    events: [created]\n    function: main/on-paid\n", "the name must be lower case"},
		"when needs caller":       {"triggers:\n  t:\n    events: [created]\n    when: \"user != nil\"\n    function: main/on-paid\n", "an event has no caller"},
		"when unknown field":      {"triggers:\n  t:\n    events: [created]\n    when: \"document.nope == 1\"\n    function: main/on-paid\n", "unknown field document.nope"},
		"when does not parse":     {"triggers:\n  t:\n    events: [created]\n    when: \"document.status ==\"\n    function: main/on-paid\n", "triggers.t: when:"},
		"removal reads document":  {"triggers:\n  t:\n    events: [removed]\n    when: \"document.status == 'paid'\"\n    function: main/on-paid\n", "could never fire"},
		"unknown field in fields": {"triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n    fields: [status, nope]\n", `fields: "nope" is not a field`},
		"star mixed":              {"triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n    fields: [\"*\", status]\n", "can't be mixed"},
		"typo inside":             {"triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n    whn: \"true\"\n", "whn"},
	}
	for name, c := range cases {
		_, err := triggerTree(t, c.yaml, "", "")
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "collection.yaml") {
			t.Errorf("%s: err = %v, want %q naming collection.yaml", name, err, c.want)
		}
	}
}

func TestTriggerFunctionMustBeInternalAndAsync(t *testing.T) {
	yaml := "triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n"
	for name, tc := range map[string]struct{ fn, want string }{
		"not internal": {"mode: async\n", "must have `internal: true`"},
		"not async":    {"internal: true\n", "must have `mode: async`"},
	} {
		_, err := triggerTree(t, yaml, tc.fn, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "triggers.t.function") {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestRealmTriggerLimits(t *testing.T) {
	yaml := "triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n"
	reg, err := triggerTree(t, yaml, "", "auth: enabled\nsignup: open\ntriggers:\n  max_chain: 4\n  max_chained_per_minute: 10\n  max_per_collection: 2\n  max_backlog: 500\n")
	if err != nil {
		t.Fatal(err)
	}
	l := reg.Realms["shop"].Settings.TriggerLimits(TriggerLimits{})
	if l.MaxChain != 4 || l.MaxChainedPerMinute != 10 || l.MaxPerCollection != 2 || l.MaxBacklog != 500 {
		t.Errorf("limits = %+v", l)
	}
	if err := reg.CheckTriggerLimits(TriggerLimits{}); err != nil {
		t.Errorf("within the instance's limits: %v", err)
	}
	// a realm may only lower the instance's depth
	if err := reg.CheckTriggerLimits(TriggerLimits{MaxChain: 3}); err == nil || !strings.Contains(err.Error(), "triggers.max_chain: 4 is above this instance's limit of 3 (BACKD_TRIGGER_MAX_CHAIN)") {
		t.Errorf("above the instance: %v", err)
	}
	// defaults
	def := RealmSettings{}.TriggerLimits(TriggerLimits{})
	if def != DefaultTriggerLimits {
		t.Errorf("defaults = %+v", def)
	}
}

func TestTriggersPerCollectionCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("triggers:\n")
	for _, n := range []string{"a", "b", "c"} {
		b.WriteString("  t_")
		b.WriteString(n)
		b.WriteString(":\n    events: [created]\n    function: main/on-paid\n")
	}
	reg, err := triggerTree(t, b.String(), "", "auth: enabled\nsignup: open\ntriggers:\n  max_per_collection: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	err = reg.CheckTriggerLimits(TriggerLimits{})
	if err == nil || !strings.Contains(err.Error(), "3 triggers, but a collection may declare at most 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestRealmTriggerSettingsValidation(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"zero":     {"auth: enabled\ntriggers:\n  max_chain: 0\n", "triggers.max_chain: must be at least 1"},
		"too deep": {"auth: enabled\ntriggers:\n  max_chain: 1000\n", "triggers.max_chain: must be at most 64"},
		"negative": {"auth: enabled\ntriggers:\n  max_backlog: -5\n", "triggers.max_backlog: must be at least 1"},
		"unknown":  {"auth: enabled\ntriggers:\n  max_depth: 3\n", "max_depth"},
	} {
		_, errs := parseRealmSettings([]byte(tc.yaml))
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Error(), tc.want)
		}
		if !found {
			t.Errorf("%s: errors %v, want %q", name, errs, tc.want)
		}
	}
}

func TestTriggersNeedAuthEnabled(t *testing.T) {
	yaml := "triggers:\n  t:\n    events: [created]\n    function: main/on-paid\n"
	_, err := triggerTree(t, yaml, "", "auth: disabled\n")
	if err == nil || !strings.Contains(err.Error(), "triggers: only apply when the realm has auth enabled") {
		t.Fatalf("err = %v", err)
	}
}
