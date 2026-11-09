// Package limits reads the instance limits that also decide whether a config
// is valid (image pixels, reference expansion, aggregation, trigger chains)
// from the environment. `backd config check` applies them without the
// server's settings, so this package never imports the server's packages.
package limits

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Expand reads the instance's reference expansion limits
// (BACKD_EXPAND_MAX_DEPTH, _PATHS, _ARRAY and _DOCS). A collection's reference may only lower
// the array limit.
func Expand(getenv func(string) string) (registry.ExpandLimits, error) {
	l := registry.DefaultExpandLimits
	var errs []error
	for _, v := range []struct {
		env  string
		dst  *int
		max  int
		hint string
	}{
		{"BACKD_EXPAND_MAX_DEPTH", &l.MaxDepth, 2, "1 or 2"},
		{"BACKD_EXPAND_MAX_PATHS", &l.MaxPaths, 0, "a positive integer"},
		{"BACKD_EXPAND_MAX_ARRAY", &l.MaxArray, 0, "a positive integer"},
		{"BACKD_EXPAND_MAX_DOCS", &l.MaxDocs, 0, "a positive integer"},
	} {
		s := getenv(v.env)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || (v.max > 0 && n > v.max) {
			errs = append(errs, fmt.Errorf("%s must be %s, got %q", v.env, v.hint, s))
			continue
		}
		*v.dst = n
	}
	return l, errors.Join(errs...)
}

// Aggregate reads the instance's aggregation limits (BACKD_AGGREGATE_TIME and
// BACKD_AGGREGATE_MAX_GROUPS). A collection's aggregate.limits may only lower them.
func Aggregate(getenv func(string) string) (registry.AggregateLimits, error) {
	l := registry.DefaultAggregateLimits
	var errs []error
	if v := getenv("BACKD_AGGREGATE_TIME"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 100*time.Millisecond || d > time.Minute {
			errs = append(errs, fmt.Errorf("BACKD_AGGREGATE_TIME must be a duration between 100ms and 1m, got %q", v))
		} else {
			l.Time = d
		}
	}
	if v := getenv("BACKD_AGGREGATE_MAX_GROUPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10000 {
			errs = append(errs, fmt.Errorf("BACKD_AGGREGATE_MAX_GROUPS must be an integer between 1 and 10000, got %q", v))
		} else {
			l.MaxGroups = n
		}
	}
	return l, errors.Join(errs...)
}

// Trigger reads the instance's trigger limits: BACKD_TRIGGER_MAX_CHAIN, how deep a
// chain of writes caused by trigger runs may go. A realm's `triggers:` may only lower it.
func Trigger(getenv func(string) string) (registry.TriggerLimits, error) {
	l := registry.DefaultTriggerLimits
	if v := getenv("BACKD_TRIGGER_MAX_CHAIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > registry.MaxTriggerChain {
			return l, fmt.Errorf("BACKD_TRIGGER_MAX_CHAIN must be an integer between 1 and %d, got %q", registry.MaxTriggerChain, v)
		}
		l.MaxChain = n
	}
	return l, nil
}

// DefaultImageMaxPixels is the pixel limit for image versions when none is set.
const DefaultImageMaxPixels = 40_000_000

// ImageMaxPixels reads BACKD_IMAGE_MAX_PIXELS: a positive number of pixels.
func ImageMaxPixels(getenv func(string) string) (int64, error) {
	v := getenv("BACKD_IMAGE_MAX_PIXELS")
	if v == "" {
		return DefaultImageMaxPixels, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("BACKD_IMAGE_MAX_PIXELS must be a positive integer, got %q", v)
	}
	return n, nil
}
