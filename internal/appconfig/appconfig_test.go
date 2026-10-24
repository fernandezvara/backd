package appconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

const testSchema = `{
  "type": "object", "additionalProperties": false,
  "properties": {
    "maintenance": {"type": "object", "additionalProperties": false, "properties": {
      "enabled": {"type": "boolean", "default": false},
      "message": {"type": "string", "maxLength": 20, "default": ""}
    }},
    "features": {"type": "object", "additionalProperties": false, "properties": {
      "reports": {"type": "object", "additionalProperties": false, "properties": {
        "enabled": {"type": "boolean", "default": true},
        "max_range": {"type": "string", "pattern": "^[0-9]+d$", "default": "365d"},
        "limit": {"type": "integer", "minimum": 1, "default": 10}
      }}
    }},
    "tags": {"type": "array", "items": {"type": "string"}, "default": []}
  }
}`

func testService(t *testing.T) (*Service, *MemStore, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"shop/realm.yaml":                "roles: {}\n",
		"shop/_realm/config/schema.json": testSchema,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := &MemStore{}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return &Service{Store: store, Config: reg.Realms["shop"].AppConfig, Now: func() time.Time { return now }}, store, &now
}

func value(t *testing.T, v View, path ...string) any {
	t.Helper()
	var cur any = v.Values
	for _, p := range path {
		cur = cur.(map[string]any)[p]
	}
	return cur
}

func TestCurrentIsTheDefaults(t *testing.T) {
	s, _, _ := testService(t)
	v, err := s.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 0 || value(t, v, "features", "reports", "max_range") != "365d" || value(t, v, "maintenance", "enabled") != false {
		t.Errorf("view = %+v", v)
	}
}

func TestSetResetAndVersions(t *testing.T) {
	s, _, now := testService(t)
	ctx := context.Background()
	r, err := s.Apply(ctx, 0, Input{Set: map[string]any{"maintenance.enabled": true, "maintenance.message": "Back at 10:00", "features.reports.limit": float64(50)}}, "user:u1")
	if err != nil || r.Version != 1 || !r.Changed || len(r.Changes) != 3 {
		t.Fatalf("apply = %+v, %v", r, err)
	}
	v, _ := s.Current(ctx)
	if value(t, v, "maintenance", "enabled") != true || value(t, v, "features", "reports", "limit") != float64(50) || v.UpdatedBy != "user:u1" {
		t.Errorf("view = %+v", v)
	}

	// A stale version loses.
	if _, err := s.Apply(ctx, 0, Input{Set: map[string]any{"maintenance.enabled": false}}, "user:u2"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale apply = %v", err)
	}

	// Setting what is already there changes nothing and makes no version.
	*now = now.Add(time.Minute)
	r, err = s.Apply(ctx, 1, Input{Set: map[string]any{"maintenance.enabled": true}}, "user:u1")
	if err != nil || r.Changed || r.Version != 1 {
		t.Errorf("no-op apply = %+v, %v", r, err)
	}

	// Reset a branch; setting a value to its default drops the override.
	r, err = s.Apply(ctx, 1, Input{Reset: []string{"maintenance"}, Set: map[string]any{"features.reports.limit": float64(10)}}, "function:main/promo")
	if err != nil || r.Version != 2 || len(r.Changes) != 3 {
		t.Fatalf("reset = %+v, %v", r, err)
	}
	v, _ = s.Current(ctx)
	if len(v.Overrides) != 0 {
		t.Errorf("overrides = %v, want none", v.Overrides)
	}

	h, _ := s.History(ctx, 10)
	if len(h) != 2 || h[0].Version != 2 || h[0].Actor != "function:main/promo" || h[1].Changes[0].Path != "features.reports.limit" {
		t.Errorf("history = %+v", h)
	}
	if c := h[0].Changes; c[0].Old == nil && c[0].Path != "" {
		t.Errorf("changes = %+v", c)
	}
}

func TestInvalidChangesApplyNothing(t *testing.T) {
	s, _, _ := testService(t)
	ctx := context.Background()
	tests := []Input{
		{Set: map[string]any{"nope": 1}},
		{Set: map[string]any{"features": map[string]any{"reports": map[string]any{}}}},
		{Set: map[string]any{"features.reports.max_range": "soon"}},
		{Set: map[string]any{"maintenance.enabled": "yes"}},
		{Set: map[string]any{"maintenance.message": "this message is far too long"}},
		{Reset: []string{"ghost"}},
	}
	for i, in := range tests {
		_, err := s.Apply(ctx, 0, in, "user:u1")
		var inv *InvalidError
		if !errors.As(err, &inv) || len(inv.Problems) == 0 {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
	if v, _ := s.Current(ctx); v.Version != 0 {
		t.Errorf("version = %d after refused changes", v.Version)
	}
	// The path of a validation problem is the key's.
	_, err := s.Apply(ctx, 0, Input{Set: map[string]any{"features.reports.max_range": "soon"}}, "x")
	var inv *InvalidError
	if !errors.As(err, &inv) || inv.Problems[0].Path != "features.reports.max_range" {
		t.Errorf("problems = %+v", inv)
	}
}

func TestRollback(t *testing.T) {
	s, _, _ := testService(t)
	ctx := context.Background()
	s.Apply(ctx, 0, Input{Set: map[string]any{"maintenance.enabled": true}}, "u")                                        // 1
	s.Apply(ctx, 1, Input{Set: map[string]any{"maintenance.enabled": false, "features.reports.limit": float64(5)}}, "u") // 2
	r, err := s.Rollback(ctx, 2, 1, "u")
	if err != nil || r.Version != 3 || len(r.Changes) != 2 {
		t.Fatalf("rollback = %+v, %v", r, err)
	}
	v, _ := s.Current(ctx)
	if value(t, v, "maintenance", "enabled") != true || value(t, v, "features", "reports", "limit") != float64(10) {
		t.Errorf("view = %v", v.Values)
	}
	h, _ := s.History(ctx, 1)
	if h[0].Kind != KindRollback || h[0].RolledBackTo != 1 {
		t.Errorf("entry = %+v", h[0])
	}
	// Version 0 is the defaults; an unknown version is refused; a stale base conflicts.
	if r, err := s.Rollback(ctx, 3, 0, "u"); err != nil || r.Version != 4 {
		t.Errorf("rollback to 0 = %+v, %v", r, err)
	}
	if _, err := s.Rollback(ctx, 4, 99, "u"); !errors.Is(err, ErrNoVersion) {
		t.Errorf("unknown version = %v", err)
	}
	if _, err := s.Rollback(ctx, 1, 1, "u"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale rollback = %v", err)
	}
}

func TestStoredValuesThatStopValidatingFallBack(t *testing.T) {
	s, store, _ := testService(t)
	ctx := context.Background()
	store.SaveAppConfig(ctx, 0, State{Version: 1, Values: map[string]any{
		"maintenance": map[string]any{"enabled": "not a bool", "message": "ok"},
		"retired":     map[string]any{"key": 1},
	}}, Entry{Version: 1}, 0, time.Time{})
	v, err := s.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if value(t, v, "maintenance", "enabled") != false || value(t, v, "maintenance", "message") != "ok" {
		t.Errorf("values = %v", v.Values)
	}
	paths := map[string]bool{}
	for _, p := range v.Invalid {
		paths[p.Path] = true
	}
	if !paths["maintenance.enabled"] || !paths["retired.key"] || len(v.Invalid) != 2 {
		t.Errorf("invalid = %+v", v.Invalid)
	}
	// A change on top of them applies and drops them.
	if _, err := s.Apply(ctx, 1, Input{Set: map[string]any{"features.reports.limit": float64(3)}}, "u"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Current(ctx); len(v.Invalid) != 0 || value(t, v, "maintenance", "message") != "ok" {
		t.Errorf("after the change: %+v", v)
	}
}

func TestHistoryRetention(t *testing.T) {
	s, store, now := testService(t)
	ctx := context.Background()
	for i := 0; i < HistoryVersions+20; i++ {
		*now = now.Add(time.Hour)
		if _, err := s.Apply(ctx, int64(i), Input{Set: map[string]any{"features.reports.limit": float64(i%50 + 11)}}, "u"); err != nil {
			t.Fatal(err)
		}
	}
	// Everything is younger than 90 days: nothing is pruned.
	if h, _ := s.History(ctx, 1000); len(h) != HistoryVersions {
		t.Fatalf("limit gives %d", len(h))
	}
	if _, err := store.AppConfigEntry(ctx, 1); err != nil {
		t.Errorf("version 1 pruned too early: %v", err)
	}
	*now = now.Add(HistoryAge + time.Hour)
	if _, err := s.Apply(ctx, int64(HistoryVersions+20), Input{Set: map[string]any{"maintenance.enabled": true}}, "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppConfigEntry(ctx, 1); !errors.Is(err, ErrNoVersion) {
		t.Errorf("version 1 kept: %v", err)
	}
	if _, err := store.AppConfigEntry(ctx, int64(HistoryVersions+20)); err != nil {
		t.Errorf("recent version pruned: %v", err)
	}
}
