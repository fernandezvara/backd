package strutil

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 720 * time.Hour, "0d": 0, "90m": 90 * time.Minute, "1h30m": 90 * time.Minute} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "1.5d", "-1d", "30", "abc"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q): expected error", in)
		}
	}
}
