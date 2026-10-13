package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/appconfig"
)

func TestAuthStoreAppConfig(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	st, err := s.AppConfigState(ctx)
	if err != nil || st.Version != 0 || len(st.Values) != 0 {
		t.Fatalf("empty state = %+v, %v", st, err)
	}
	save := func(expected int64, values map[string]any, at time.Time) error {
		v := expected + 1
		return s.SaveAppConfig(ctx,
			expected,
			appconfig.State{Version: v, Values: values, UpdatedAt: at, UpdatedBy: "user:u1"},
			appconfig.Entry{Version: v, At: at, Actor: "user:u1", Kind: appconfig.KindSet, Values: values,
				Changes: []appconfig.Change{{Path: "a.b", Old: float64(v - 1), New: float64(v)}}},
			v-appconfig.HistoryVersions, at.Add(-appconfig.HistoryAge))
	}
	if err := save(0, map[string]any{"a": map[string]any{"b": float64(1), "list": []any{"x"}}}, now); err != nil {
		t.Fatal(err)
	}
	// The same base can't be used twice, from nothing or from a version.
	if err := save(0, map[string]any{"a": map[string]any{"b": float64(9)}}, now); !errors.Is(err, appconfig.ErrConflict) {
		t.Errorf("second first version = %v", err)
	}
	if err := save(1, map[string]any{"a": map[string]any{"b": float64(2)}}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := save(1, map[string]any{"a": map[string]any{"b": float64(7)}}, now.Add(time.Minute)); !errors.Is(err, appconfig.ErrConflict) {
		t.Errorf("stale save = %v", err)
	}
	st, err = s.AppConfigState(ctx)
	if err != nil || st.Version != 2 || st.Values["a"].(map[string]any)["b"] != float64(2) || st.UpdatedBy != "user:u1" {
		t.Fatalf("state = %+v, %v", st, err)
	}

	h, err := s.AppConfigHistory(ctx, 10)
	if err != nil || len(h) != 2 || h[0].Version != 2 || h[1].Version != 1 || h[0].Changes[0].Path != "a.b" || h[0].Changes[0].Old != float64(1) {
		t.Fatalf("history = %+v, %v", h, err)
	}
	e, err := s.AppConfigEntry(ctx, 1)
	if err != nil || e.Values["a"].(map[string]any)["list"].([]any)[0] != "x" || !e.At.Equal(now) {
		t.Errorf("entry 1 = %+v, %v", e, err)
	}
	if _, err := s.AppConfigEntry(ctx, 5); !errors.Is(err, appconfig.ErrNoVersion) {
		t.Errorf("unknown entry = %v", err)
	}

	// Old entries beyond the kept versions go once they are old enough.
	for v := int64(2); v < appconfig.HistoryVersions+3; v++ {
		if err := save(v, map[string]any{"a": map[string]any{"b": float64(v + 1)}}, now.Add(time.Duration(v)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppConfigEntry(ctx, 1); err != nil {
		t.Errorf("an entry younger than the age was pruned: %v", err)
	}
	last := now.Add(appconfig.HistoryAge + 1000*time.Hour)
	if err := save(appconfig.HistoryVersions+3, map[string]any{"a": map[string]any{"b": float64(1)}}, last); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppConfigEntry(ctx, 1); !errors.Is(err, appconfig.ErrNoVersion) {
		t.Errorf("an old entry beyond the kept versions stayed: %v", err)
	}
	if _, err := s.AppConfigEntry(ctx, appconfig.HistoryVersions+4); err != nil {
		t.Errorf("the latest entry is gone: %v", err)
	}
}
