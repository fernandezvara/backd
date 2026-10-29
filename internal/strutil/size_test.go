package strutil

import (
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1B": 1, "2KB": 2000, "128MB": 128_000_000, "1GB": 1e9, "4KiB": 4096, "1MiB": 1 << 20, "2GiB": 2 << 30} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "12", "1.5MB", "1 MB", "1mb", "-1MB", "99999999999GiB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) accepted", in)
		}
	}
}
