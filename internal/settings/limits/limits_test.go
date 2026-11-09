package limits

import (
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

func TestImageMaxPixels(t *testing.T) {
	if n, err := ImageMaxPixels(env(nil)); err != nil || n != 40_000_000 {
		t.Errorf("default = %d, %v", n, err)
	}
	if n, err := ImageMaxPixels(env(map[string]string{"BACKD_IMAGE_MAX_PIXELS": "1000000"})); err != nil || n != 1_000_000 {
		t.Errorf("set = %d, %v", n, err)
	}
	for _, bad := range []string{"0", "-5", "many", "1.5"} {
		if _, err := ImageMaxPixels(env(map[string]string{"BACKD_IMAGE_MAX_PIXELS": bad})); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestExpandLimits(t *testing.T) {
	if l, err := Expand(env(nil)); err != nil || l != registry.DefaultExpandLimits {
		t.Fatalf("default = %+v, %v", l, err)
	}
	l, err := Expand(env(map[string]string{"BACKD_EXPAND_MAX_ARRAY": "25", "BACKD_EXPAND_MAX_DEPTH": "1", "BACKD_EXPAND_MAX_PATHS": "3", "BACKD_EXPAND_MAX_DOCS": "500"}))
	if want := (registry.ExpandLimits{MaxDepth: 1, MaxPaths: 3, MaxArray: 25, MaxDocs: 500}); err != nil || l != want {
		t.Fatalf("set = %+v, %v", l, err)
	}
	for _, kv := range [][2]string{{"BACKD_EXPAND_MAX_ARRAY", "0"}, {"BACKD_EXPAND_MAX_DOCS", "-3"}, {"BACKD_EXPAND_MAX_PATHS", "many"}, {"BACKD_EXPAND_MAX_DEPTH", "3"}} {
		if _, err := Expand(env(map[string]string{kv[0]: kv[1]})); err == nil || !strings.Contains(err.Error(), kv[0]) {
			t.Errorf("%s=%s: err = %v", kv[0], kv[1], err)
		}
	}
}

func TestAggregateLimits(t *testing.T) {
	if l, err := Aggregate(env(nil)); err != nil || l != registry.DefaultAggregateLimits {
		t.Fatalf("default = %+v, %v", l, err)
	}
	l, err := Aggregate(env(map[string]string{"BACKD_AGGREGATE_TIME": "2s", "BACKD_AGGREGATE_MAX_GROUPS": "200"}))
	if want := (registry.AggregateLimits{Time: 2 * time.Second, MaxGroups: 200}); err != nil || l != want {
		t.Fatalf("set = %+v, %v", l, err)
	}
	for _, kv := range [][2]string{{"BACKD_AGGREGATE_TIME", "0s"}, {"BACKD_AGGREGATE_TIME", "soon"}, {"BACKD_AGGREGATE_TIME", "5m"}, {"BACKD_AGGREGATE_MAX_GROUPS", "0"}, {"BACKD_AGGREGATE_MAX_GROUPS", "99999"}} {
		if _, err := Aggregate(env(map[string]string{kv[0]: kv[1]})); err == nil || !strings.Contains(err.Error(), kv[0]) {
			t.Errorf("%s=%s: err = %v", kv[0], kv[1], err)
		}
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
