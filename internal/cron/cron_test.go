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
