// Package cron parses five-field cron expressions and computes their
// schedule, always in UTC.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool
}

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	fMinute = field{name: "minute", min: 0, max: 59}
	fHour   = field{name: "hour", min: 0, max: 23}
	fDom    = field{name: "day of month", min: 1, max: 31}
	fMonth  = field{name: "month", min: 1, max: 12, names: map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}}
	fDow    = field{name: "day of week", min: 0, max: 7, names: map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}}
)

var descriptors = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// Parse parses "minute hour day-of-month month day-of-week" (each field:
// *, a number, a range a-b, a list, and /step; month and weekday names
// work) or one of @hourly, @daily, @weekly, @monthly, @yearly. As in
// classic cron, when both day fields are restricted a time matches if
// either does.
func Parse(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if d, ok := descriptors[expr]; ok {
		expr = d
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("want 5 fields (minute hour day-of-month month day-of-week) or @hourly, @daily, @weekly, @monthly, @yearly; got %q", expr)
	}
	s := &Schedule{domStar: strings.HasPrefix(parts[2], "*"), dowStar: strings.HasPrefix(parts[4], "*")}
	var err error
	for i, p := range []struct {
		f   field
		dst *uint64
	}{{fMinute, &s.minute}, {fHour, &s.hour}, {fDom, &s.dom}, {fMonth, &s.month}, {fDow, &s.dow}} {
		if *p.dst, err = parseField(parts[i], p.f); err != nil {
			return nil, err
		}
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday too
		s.dow |= 1
	}
	return s, nil
}

func parseField(s string, f field) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(s, ",") {
		rng, step := item, 1
		if r, st, ok := strings.Cut(item, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%s: invalid step %q", f.name, st)
			}
			rng, step = r, n
		}
		lo, hi := f.min, f.max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = value(a, f); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = value(b, f); err != nil {
					return 0, err
				}
			} else if step > 1 {
				hi = f.max
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: range %q goes backwards", f.name, rng)
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func value(s string, f field) (int, error) {
	if n, ok := f.names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < f.min || n > f.max {
		return 0, fmt.Errorf("%s: %q must be between %d and %d", f.name, s, f.min, f.max)
	}
	return n, nil
}

func (s *Schedule) matchesDay(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar || s.dowStar:
		return dom && dow
	default:
		return dom || dow
	}
}

func (s *Schedule) matches(t time.Time) bool {
	return s.minute&(1<<uint(t.Minute())) != 0 && s.hour&(1<<uint(t.Hour())) != 0 &&
		s.month&(1<<uint(t.Month())) != 0 && s.matchesDay(t)
}

// Prev returns the latest scheduled time at or before t (UTC, minute
// resolution), or false if none exists within about eight years back.
func (s *Schedule) Prev(t time.Time) (time.Time, bool) {
	t = t.UTC().Truncate(time.Minute)
	for i := 0; i < 8*366*24*60; i++ {
		if s.matches(t) {
			return t, true
		}
		// Skip whole days or hours that cannot match.
		switch {
		case s.month&(1<<uint(t.Month())) == 0 || !s.matchesDay(t):
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Add(-time.Minute)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = t.Truncate(time.Hour).Add(-time.Minute)
		default:
			t = t.Add(-time.Minute)
		}
	}
	return time.Time{}, false
}
