package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/rules"
)

// The events of a trigger, in the vocabulary realtime shares (design/triggers-and-realtime.md §1).
const (
	TriggerCreated = "created"
	TriggerUpdated = "updated"
	TriggerRemoved = "removed"
)

var triggerEvents = []string{TriggerCreated, TriggerUpdated, TriggerRemoved}

// Trigger is a collection's reactive rule: when a document changes in one of the listed
// ways and When holds, backd runs Function in the background (design §7).
type Trigger struct {
	Name   string
	Events []string // created, updated, removed; sorted, at least one
	// When is the compiled condition over `document`, `before`, `changed()`, `now` and
	// appConfig; nil fires on every listed event.
	When *rules.Rule
	// Function is the internal, async function to run, as <database>/<function>.
	Function string
	// Fields says what rides in the job: none (ids, the version and what changed), a list of
	// paths of the documents before and after, or ["*"] for them whole.
	Fields []string
	// Session adds `by`, the user who wrote, to the input; Ordered runs the jobs of one
	// document one at a time.
	Session bool
	Ordered bool
	// File is the collection.yaml it is declared in, for messages.
	File string
}

// FiresOn reports whether the trigger listens to the event.
func (t *Trigger) FiresOn(event string) bool { return slices.Contains(t.Events, event) }

// triggerDoc is one entry of collection.yaml's triggers:.
type triggerDoc struct {
	Events   []string `yaml:"events"`
	When     string   `yaml:"when"`
	Function string   `yaml:"function"`
	Fields   []string `yaml:"fields"`
	Session  bool     `yaml:"session"`
	Ordered  bool     `yaml:"ordered"`
}

// TriggerLimits are the bounds on triggers, from the instance (BACKD_TRIGGER_MAX_CHAIN) and
// lowered by a realm's realm.yaml `triggers:`.
type TriggerLimits struct {
	// MaxChain is how deep a chain of writes caused by trigger runs may go.
	MaxChain int
	// MaxChainedPerMinute caps, per trigger, the chained events it handles in a minute.
	MaxChainedPerMinute int
	// MaxPerCollection is how many triggers one collection may declare.
	MaxPerCollection int
	// MaxBacklog is how many queued trigger jobs a realm may have before further events are
	// recorded as a gap instead of queued.
	MaxBacklog int
}

// DefaultTriggerLimits are the limits when nothing sets them.
var DefaultTriggerLimits = TriggerLimits{MaxChain: 16, MaxChainedPerMinute: 60, MaxPerCollection: 20, MaxBacklog: 100000}

// MaxTriggerChain is the highest depth any configuration may allow.
const MaxTriggerChain = 64

// OrDefault fills the limits that are zero with the defaults.
func (l TriggerLimits) OrDefault() TriggerLimits {
	d := DefaultTriggerLimits
	if l.MaxChain <= 0 {
		l.MaxChain = d.MaxChain
	}
	if l.MaxChainedPerMinute <= 0 {
		l.MaxChainedPerMinute = d.MaxChainedPerMinute
	}
	if l.MaxPerCollection <= 0 {
		l.MaxPerCollection = d.MaxPerCollection
	}
	if l.MaxBacklog <= 0 {
		l.MaxBacklog = d.MaxBacklog
	}
	return l
}

// TriggerLimits returns the limits in force for a realm: the instance's, lowered by what the
// realm says (never raised: CheckTriggerLimits refuses a realm that tries).
func (s RealmSettings) TriggerLimits(instance TriggerLimits) TriggerLimits {
	l := instance.OrDefault()
	t := s.Triggers
	if t.MaxChain > 0 {
		l.MaxChain = min(l.MaxChain, t.MaxChain)
	}
	if t.MaxPerCollection > 0 {
		l.MaxPerCollection = min(l.MaxPerCollection, t.MaxPerCollection)
	}
	if t.MaxChainedPerMinute > 0 {
		l.MaxChainedPerMinute = t.MaxChainedPerMinute
	}
	if t.MaxBacklog > 0 {
		l.MaxBacklog = t.MaxBacklog
	}
	return l
}

// loadTriggers reads collection.yaml's `triggers:` into c and checks it: names, events, the
// condition against the collection's schema, and the fields the job carries. The function a
// trigger names is checked once the realm's functions are loaded (loadTriggerFunctions).
func loadTriggers(c *Collection, settings RealmSettings, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		Triggers map[string]*triggerDoc `yaml:"triggers"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it; the keys inside
		// a trigger are checked, with the file named, by loadErasure.
		return nil
	}
	if len(doc.Triggers) == 0 {
		return nil
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(doc.Triggers)) {
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: triggers.%s: "+format, append([]any{path, name}, args...)...))
		}
		d := doc.Triggers[name]
		if d == nil {
			fail("is empty: declare `events` and `function`")
			continue
		}
		t := &Trigger{Name: name, Function: d.Function, Session: d.Session, Ordered: d.Ordered, File: path}
		if !namePattern.MatchString(name) {
			fail("the name must be lower case letters and digits, with - or _ between them")
		}
		t.Events = checkTriggerEvents(d.Events, fail)
		t.Fields = checkTriggerFields(c, d.Fields, fail)
		if db, fn, ok := strings.Cut(d.Function, "/"); d.Function == "" {
			fail("function is required: the internal, async function to run, as <database>/<function>")
		} else if !ok || !namePattern.MatchString(db) || !namePattern.MatchString(fn) {
			fail("function %q must be <database>/<function>", d.Function)
		}
		if strings.TrimSpace(d.When) != "" {
			r, err := rules.CompileTrigger(d.When, c.ruleSchema(settings))
			if err != nil {
				fail("when: %v", err)
			} else {
				t.When = r
				if len(t.Events) == 1 && t.Events[0] == TriggerRemoved && r.References("document") {
					fail("when: names `document`, which a removal doesn't have: it could never fire; read `before` for removals")
				}
			}
		} else if d.When != "" {
			fail("when: is empty: leave it out to fire on every listed event")
		}
		c.Triggers = append(c.Triggers, t)
	}
	if len(errs) > 0 {
		c.Triggers = nil
		return errors.Join(errs...)
	}
	return nil
}

func checkTriggerEvents(in []string, fail func(string, ...any)) []string {
	if len(in) == 0 {
		fail("events: list at least one of %s", strings.Join(triggerEvents, ", "))
		return nil
	}
	var out []string
	for _, e := range in {
		switch {
		case !slices.Contains(triggerEvents, e):
			fail("events: %q is not an event: want %s", e, strings.Join(triggerEvents, ", "))
		case slices.Contains(out, e):
			fail("events: %q is listed twice", e)
		default:
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out
}

func checkTriggerFields(c *Collection, in []string, fail func(string, ...any)) []string {
	if len(in) == 0 {
		return nil
	}
	if slices.Contains(in, "*") {
		if len(in) > 1 {
			fail("fields: \"*\" (whole documents) can't be mixed with field names")
			return nil
		}
		return []string{"*"}
	}
	var out []string
	for _, f := range in {
		switch {
		case !c.IsKnownField(f):
			fail("fields: %q is not a field of the collection (a path of schema.json, or id and _meta fields)", f)
		case slices.Contains(out, f):
			fail("fields: %q is listed twice", f)
		default:
			out = append(out, f)
		}
	}
	return out
}

// loadTriggerFunctions checks that every trigger names an internal, async function of its
// realm: nobody can call it over HTTP, and nothing waits for it while a document changes.
func loadTriggerFunctions(rl *Realm) []error {
	var errs []error
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			for _, t := range c.Triggers {
				if _, err := hookFunction(rl, c.ConfigPath(), "triggers."+t.Name+".function", t.Function, ModeAsync, "nothing waits for it while a document changes"); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errs
}

// CheckTriggerLimits checks the configuration against this instance's limits: a realm may
// only lower the depth of chains and the triggers per collection, and no collection may
// declare more triggers than its realm allows.
func (r *Registry) CheckTriggerLimits(instance TriggerLimits) error {
	instance = instance.OrDefault()
	var errs []error
	for _, name := range r.RealmNames() {
		rl := r.Realms[name]
		file := filepath.Join(r.Root, name, RealmFile)
		t := rl.Settings.Triggers
		if t.MaxChain > instance.MaxChain {
			errs = append(errs, fmt.Errorf("%s: triggers.max_chain: %d is above this instance's limit of %d (BACKD_TRIGGER_MAX_CHAIN); a realm can only lower it", file, t.MaxChain, instance.MaxChain))
		}
		if t.MaxPerCollection > instance.MaxPerCollection {
			errs = append(errs, fmt.Errorf("%s: triggers.max_per_collection: %d is above this instance's limit of %d; a realm can only lower it", file, t.MaxPerCollection, instance.MaxPerCollection))
		}
		limit := rl.Settings.TriggerLimits(instance).MaxPerCollection
		for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
			for _, c := range rl.Databases[dbName].SortedCollections() {
				if n := len(c.Triggers); n > limit {
					errs = append(errs, fmt.Errorf("%s: triggers: %d triggers, but a collection may declare at most %d (triggers.max_per_collection)", c.ConfigPath(), n, limit))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// TriggerSettings is realm.yaml's `triggers:`; 0 leaves the instance's value.
type TriggerSettings struct {
	MaxChain            int
	MaxChainedPerMinute int
	MaxPerCollection    int
	MaxBacklog          int
}
