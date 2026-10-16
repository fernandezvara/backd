package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
)

// docSource is DocSource over a slice.
type docSource struct {
	docs []map[string]any
}

func (d *docSource) Count(context.Context, string, string, time.Time, time.Time) (int64, error) {
	return int64(len(d.docs)), nil
}

func (d *docSource) Updated(_ context.Context, _, _ string, _, _ time.Time, fn func(map[string]any) error) error {
	for _, doc := range d.docs {
		if err := fn(doc); err != nil {
			return err
		}
	}
	return nil
}

func rescanDoc(id, status string, version int64) map[string]any {
	return map[string]any{"id": id, "status": status, "_meta": map[string]any{"version": version, "updated_at": t0}}
}

const rescanTriggers = `triggers:
  on_paid:
    events: [created, updated]
    when: "document.status == 'paid'"
    function: main/on-paid
  needs_before:
    events: [updated]
    when: "before.status != 'paid' && document.status == 'paid'"
    function: main/on-paid
  only_created:
    events: [created]
    function: main/on-paid
  always:
    events: [updated]
    function: main/on-paid
    fields: [status]
`

func TestRescanQueuesWhatCanBeEvaluatedFromADocument(t *testing.T) {
	reg := testRegistry(t, rescanTriggers)
	src := &docSource{docs: []map[string]any{rescanDoc("o1", "paid", 3), rescanDoc("o2", "draft", 1)}}
	q := newQueue()
	res, err := Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Since: t0, Run: "run-1"}, src, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	// on_paid fires for o1 only; always fires for both; the other two are not rescannable.
	if res.Scanned != 2 || res.Queued != 3 || res.Duplicates != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.NotRescannable["needs_before"] == "" || res.NotRescannable["only_created"] == "" || len(res.NotRescannable) != 2 {
		t.Errorf("not rescannable = %v", res.NotRescannable)
	}
	for _, j := range q.list() {
		var in map[string]any
		_ = json.Unmarshal(j.Input, &in)
		if in["rescan"] != "run-1" || in["event"] != "updated" || in["depth"] != float64(0) {
			t.Errorf("input = %v", in)
		}
		if j.Origin == "trigger:main/orders/always" {
			after := in["after"].(map[string]any)
			if after["status"] == nil || in["before"] != nil {
				t.Errorf("bodies: before=%v after=%v", in["before"], after)
			}
		}
	}

	// The same run again collapses; another run is new work with ids of its own.
	res, _ = Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Since: t0, Run: "run-1"}, src, q, nil)
	if res.Queued != 0 || res.Duplicates != 3 {
		t.Errorf("same run again: %+v", res)
	}
	res, _ = Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Since: t0, Run: "run-2"}, src, q, nil)
	if res.Queued != 3 || q.size() != 6 {
		t.Errorf("another run: %+v, %d jobs", res, q.size())
	}
}

func TestRescanOfOneTriggerAndUnknownOnes(t *testing.T) {
	reg := testRegistry(t, rescanTriggers)
	src := &docSource{docs: []map[string]any{rescanDoc("o1", "paid", 1)}}
	q := newQueue()
	res, err := Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Trigger: "on_paid", Since: t0, Run: "r"}, src, q, nil)
	if err != nil || res.Queued != 1 || len(res.NotRescannable) != 0 {
		t.Fatalf("one trigger: %+v %v", res, err)
	}
	if _, err := Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Trigger: "nope", Since: t0}, src, q, nil); err == nil {
		t.Error("an unknown trigger must be an error")
	}
	if _, err := Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "nope", Since: t0}, src, q, nil); err == nil {
		t.Error("an unknown collection must be an error")
	}
	res, err = Rescan(context.Background(), reg, RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Trigger: "needs_before", Since: t0}, src, q, nil)
	if err != nil || res.Queued != 0 || res.NotRescannable["needs_before"] == "" {
		t.Errorf("a trigger that needs before: %+v %v", res, err)
	}
}

func TestRescanAsksForConfirmationOverTheThreshold(t *testing.T) {
	reg := testRegistry(t, "triggers:\n  on_paid:\n    events: [updated]\n    when: \"document.status == 'paid'\"\n    function: main/on-paid\n")
	var docs []map[string]any
	for i := 0; i < RescanConfirmOver+1; i++ {
		docs = append(docs, rescanDoc("o"+strconv.Itoa(i), "paid", 1))
	}
	src := &docSource{docs: docs}
	q := newQueue()
	req := RescanRequest{Realm: "shop", Database: "main", Collection: "orders", Since: t0, Run: "r"}
	_, err := Rescan(context.Background(), reg, req, src, q, nil)
	var need *ErrRescanNeedsConfirm
	if !errors.As(err, &need) || need.Documents != int64(RescanConfirmOver+1) {
		t.Fatalf("err = %v", err)
	}
	if q.size() != 0 {
		t.Fatal("jobs were queued before the confirmation")
	}
	req.Confirm = true
	res, err := Rescan(context.Background(), reg, req, src, q, nil)
	if err != nil || res.Queued != RescanConfirmOver+1 {
		t.Fatalf("confirmed: %+v %v", res, err)
	}
}
