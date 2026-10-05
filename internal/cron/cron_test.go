package cron

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPrev(t *testing.T) {
	cases := []struct{ expr, now, want string }{
		{"* * * * *", "2026-09-29 10:07", "2026-09-29 10:07"},
		{"*/15 * * * *", "2026-09-29 10:07", "2026-09-29 10:00"},
		{"0 3 * * *", "2026-09-29 02:00", "2026-09-28 03:00"},
		{"0 3 * * *", "2026-09-29 12:30", "2026-09-29 03:00"},
		{"30 4 1 * *", "2026-09-29 12:30", "2026-09-01 04:30"},
		{"0 0 * * mon", "2026-09-29 12:30", "2026-09-28 00:00"}, // 2026-09-28 is a Monday
		{"0 0 * * 7", "2026-09-29 12:30", "2026-09-27 00:00"},
		{"0 9-17/4 * * *", "2026-09-29 16:00", "2026-09-29 13:00"},
		{"0 0 29 feb *", "2026-09-29 00:00", "2024-02-29 00:00"},
		{"@hourly", "2026-09-29 10:07", "2026-09-29 10:00"},
		{"0 0 1,15 * 1", "2026-09-29 00:00", "2026-09-28 00:00"}, // either day field
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got, ok := s.Prev(at(c.now))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%s at %s: got %v %v, want %s", c.expr, c.now, got, ok, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * * * * *"} {
		if _, err := Parse(e); err == nil {
			t.Errorf("%q: want an error", e)
		}
	}
}

func TestPrevInATimeZone(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid") // clocks: forward 2026-03-29 01:00 UTC, back 2026-10-25 01:00 UTC
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, expr, now, want string }{
		{"summer", "0 9 * * *", "2026-07-01 12:00", "2026-07-01 07:00"},
		{"winter", "0 9 * * *", "2026-01-10 12:00", "2026-01-10 08:00"},
		{"before the day's time", "0 9 * * *", "2026-07-01 06:59", "2026-06-30 07:00"},
		{"across the change", "0 12 * * *", "2026-03-30 09:00", "2026-03-29 10:00"},
		{"the day before a gap", "30 2 * * *", "2026-03-29 00:30", "2026-03-28 01:30"},
		{"a time in the gap runs after it", "30 2 * * *", "2026-03-29 05:00", "2026-03-29 01:00"},
		{"a repeated time runs once, the first", "30 2 * * *", "2026-10-25 05:00", "2026-10-25 00:30"},
		{"the first of the repeated times", "30 2 * * *", "2026-10-25 01:00", "2026-10-25 00:30"},
		{"every hour runs through the repeat", "30 * * * *", "2026-10-25 01:45", "2026-10-25 01:30"},
		{"a time after the repeat", "0 3 * * *", "2026-10-25 12:00", "2026-10-25 02:00"},
		{"weekday in local time", "0 0 * * mon", "2026-09-29 12:00", "2026-09-27 22:00"}, // Monday 00:00 CEST
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.In(madrid).Prev(at(c.now))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%s: %s at %s: got %v %v, want %s", c.name, c.expr, c.now, got, ok, c.want)
		}
	}
}
