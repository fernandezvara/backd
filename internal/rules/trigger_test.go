package rules

import (
	"strings"
	"testing"
	"time"
)

func TestCompileTriggerAcceptsTheEventVocabulary(t *testing.T) {
	for _, src := range []string{
		`document.status == 'paid' && before.status != 'paid'`,
		`'status' in changed()`,
		`document.status == 'merged' && 'status' in changed()`,
		`before.status == 'draft'`,
		`document._meta.created_at < now`,
		`document.status == 'paid' && now - document._meta.updated_at < duration('1h')`,
		`len(changed()) > 0`,
		`document.status in ['paid', 'shipped']`,
		`true`,
	} {
		if _, err := CompileTrigger(src, testSchema); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestCompileTriggerRefusesWhatNeedsACaller(t *testing.T) {
	cases := []struct{ src, want string }{
		{`user != nil && document.status == 'paid'`, "an event has no caller"},
		{`data.status == 'paid'`, "an event has no caller"},
		{`hasRole(user, 'admin')`, "not available in a trigger's `when`"},
		{`hasConsent(user, 'terms')`, "not available in a trigger's `when`"},
		{`inOrg(document._meta.org)`, "not available in a trigger's `when`"},
		{`document.nope == 1`, "unknown field document.nope"},
		{`before.nope == 1`, "unknown field before.nope"},
		{`document.title +`, ""},
		{``, "empty rule"},
	}
	for _, c := range cases {
		_, err := CompileTrigger(c.src, testSchema)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.src, err, c.want)
		}
	}
}

func TestBeforeIsOnlyForTriggers(t *testing.T) {
	_, errs := parseYAML("update: before.status == 'draft'\n", testSchema)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "`before` is only available in a trigger's `when`") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestChangedIsForUpdateRulesAndTriggers(t *testing.T) {
	_, errs := parseYAML("read: len(changed()) > 0\n", testSchema)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "changed() is only available on update, and in a trigger's `when`") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestFiresTransitions(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	r, err := CompileTrigger(`document.status == 'paid' && before.status != 'paid'`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	doc := func(status string) map[string]any { return map[string]any{"id": "1", "status": status} }
	cases := []struct {
		name          string
		before, after map[string]any
		want          bool
	}{
		{"created paid fires: it came from nothing", nil, doc("paid"), true},
		{"draft to paid fires", doc("draft"), doc("paid"), true},
		{"paid again does not re-fire", doc("paid"), doc("paid"), false},
		{"paid to shipped does not fire", doc("paid"), doc("shipped"), false},
		{"a removal never matches a rule naming document", doc("draft"), nil, false},
	}
	for _, c := range cases {
		got, err := r.Fires(c.before, c.after, at, nil)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v want %v", c.name, got, err, c.want)
		}
	}
}

func TestFiresRemovalsReadBefore(t *testing.T) {
	r, err := CompileTrigger(`before.status == 'paid'`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Fires(map[string]any{"status": "paid"}, nil, time.Now(), nil)
	if err != nil || !got {
		t.Fatalf("a removal of a paid document: %v, %v", got, err)
	}
	got, _ = r.Fires(map[string]any{"status": "draft"}, nil, time.Now(), nil)
	if got {
		t.Fatal("a draft removal fired")
	}
}

func TestFiresChangedAndNow(t *testing.T) {
	r, err := CompileTrigger(`'status' in changed() && !('title' in changed())`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]any{"title": "a", "status": "draft", "_meta": map[string]any{"version": int64(1)}}
	after := map[string]any{"title": "a", "status": "paid", "_meta": map[string]any{"version": int64(2)}}
	if got, err := r.Fires(before, after, time.Now(), nil); err != nil || !got {
		t.Fatalf("status only: %v, %v", got, err)
	}
	after["title"] = "b"
	if got, _ := r.Fires(before, after, time.Now(), nil); got {
		t.Fatal("title changed too; must not fire")
	}

	// now is the change's time, not the clock: the same change reads the same later.
	r, err = CompileTrigger(`document._meta.created_at < now`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	doc := map[string]any{"_meta": map[string]any{"created_at": created}}
	if got, err := r.Fires(nil, doc, created.Add(time.Hour), nil); err != nil || !got {
		t.Fatalf("after the creation time: %v, %v", got, err)
	}
	if got, _ := r.Fires(nil, doc, created.Add(-time.Hour), nil); got {
		t.Fatal("a change older than the document's creation matched")
	}
}

func TestReferences(t *testing.T) {
	r, _ := CompileTrigger(`before.status == 'x'`, testSchema)
	if r.References("document") || !r.References("before") {
		t.Errorf("document=%v before=%v", r.References("document"), r.References("before"))
	}
}

// A `when` runs inside the dispatcher, which serves every realm of the deployment: an
// expression that tries to burn memory or time is stopped by the engine's memory budget
// instead of stalling everyone's triggers (security review of triggers, 27.21).
func TestTriggerConditionsAreResourceBounded(t *testing.T) {
	for _, src := range []string{
		`all(1..3000, all(1..3000, # >= 0))`,
		`len(1..100000000) > 0`,
		`all(1..100000, all(1..1000, # >= 0))`,
	} {
		r, err := CompileTrigger(src, Schema{})
		if err != nil {
			continue // refused at startup is as good
		}
		start := time.Now()
		_, err = r.Fires(nil, map[string]any{"a": 1}, time.Now(), nil)
		if err == nil {
			t.Errorf("%s: ran to completion", src)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: took %v to be stopped", src, d)
		}
	}
}
