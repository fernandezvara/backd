package httpapi

import (
	"context"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestAdminTriggersStatusGapsAndResolve(t *testing.T) {
	f := newRulesFixture(t, func(cfg *Config) { cfg.Triggers = &fakeTriggerAdmin{} })
	c, ok := f.reg.Collection("acme", "app", "notes")
	if !ok {
		t.Fatal("no notes collection")
	}
	c.Triggers = []*registry.Trigger{
		{Name: "on_paid", Events: []string{"created", "updated"}, Function: "app/echo", Fields: []string{"title"}, Session: true, Ordered: true},
		{Name: "audit", Events: []string{"removed"}, Function: "app/echo"},
	}
	const ad = "/v1/acme/_admin/triggers"

	code, out := f.as(t, f.key, "GET", ad+"/status", "")
	if code != 200 {
		t.Fatalf("status: %d %v", code, out)
	}
	d, _ := out["dispatcher"].(map[string]any)
	if d["held"] != true || d["epoch"] != float64(3) || d["started"] != true || d["watermark_age_seconds"] == nil {
		t.Errorf("dispatcher = %v", d)
	}
	if _, has := d["holder"]; has {
		t.Error("the lease holder's name is part of a realm admin's answer")
	}
	ts, _ := out["triggers"].([]any)
	if len(ts) != 2 {
		t.Fatalf("triggers = %v", out["triggers"])
	}
	first := ts[0].(map[string]any) // sorted by name: audit, on_paid
	second := ts[1].(map[string]any)
	if first["name"] != "audit" || second["name"] != "on_paid" || second["collection"] != "notes" || second["database"] != "app" ||
		second["function"] != "app/echo" || second["ordered"] != true || second["session"] != true || second["runs_with_admin_access"] != true ||
		first["when"] != nil || first["fields"] != nil {
		t.Errorf("triggers = %v", ts)
	}
	if f := second["fields"].([]any); len(f) != 1 || f[0] != "title" {
		t.Errorf("fields = %v", second["fields"])
	}
	if out["open_gaps"] != float64(1) {
		t.Errorf("open_gaps = %v", out["open_gaps"])
	}

	code, out = f.as(t, f.key, "GET", ad+"/gaps", "")
	items, _ := out["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("gaps: %d %v", code, out)
	}
	g := items[0].(map[string]any)
	if g["id"] != "gap1" || g["reason"] != "preimage_missing" || g["realm"] != "acme" || g["trigger"] != "on_paid" || g["resolved_at"] != nil || len(g["keys"].([]any)) != 2 {
		t.Errorf("gap = %v", g)
	}

	if code, out := f.as(t, f.ada, "POST", ad+"/gaps/gap1/resolve", ""); code != 403 {
		t.Fatalf("a user resolved a gap: %d %v", code, out)
	}
	code, out = f.as(t, f.key, "POST", ad+"/gaps/gap1/resolve", "")
	if code != 200 || out["id"] != "gap1" || out["resolved_at"] == nil {
		t.Fatalf("resolve: %d %v", code, out)
	}
	recs, _, err := f.svc.AuditTrail(context.Background(), auth.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		found = found || (r.Action == auth.AuditTriggerGapResolve && r.Target == "trigger-gap:gap1")
	}
	if !found {
		t.Errorf("no audit record of the resolution: %+v", recs)
	}
	// Resolving again, or an unknown id, is a 404; the listing no longer shows it.
	if code, _ := f.as(t, f.key, "POST", ad+"/gaps/gap1/resolve", ""); code != 404 {
		t.Errorf("twice: %d", code)
	}
	if _, out := f.as(t, f.key, "GET", ad+"/gaps", ""); len(out["items"].([]any)) != 0 {
		t.Errorf("open gaps after resolve: %v", out)
	}
	if _, out := f.as(t, f.key, "GET", ad+"/gaps?all=true", ""); len(out["items"].([]any)) != 1 {
		t.Errorf("all gaps: %v", out)
	}
}

// An instance that can't read the dispatcher's state still answers: the realm's own triggers.
func TestAdminTriggersStatusWithoutDispatcherState(t *testing.T) {
	f := newRulesFixture(t)
	code, out := f.as(t, f.key, "GET", "/v1/acme/_admin/triggers/status", "")
	if code != 200 || out["dispatcher"] != nil || out["open_gaps"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := f.as(t, f.key, "GET", "/v1/acme/_admin/triggers/gaps", ""); code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("gaps: %d %v", code, out)
	}
	if code, _ := f.as(t, f.key, "POST", "/v1/acme/_admin/triggers/gaps/x/resolve", ""); code != 404 {
		t.Errorf("resolve: %d", code)
	}
}
