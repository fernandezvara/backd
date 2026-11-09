package buildinfo

import "testing"

func TestCompare(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.9.7", "v0.9.7", 0, true},
		{"v0.9.7", "v0.10.0", -1, true},
		{"v1.0.0", "v0.99.99", 1, true},
		{"v1.0.0-rc.1", "v1.0.0", -1, true},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1, true},
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1, true},
		{"v1.0.0-beta.2", "v1.0.0-beta.11", -1, true},
		{"v1.0.0-rc.1", "v1.0.0-beta", 1, true},
		{"v1.0.0+build.5", "v1.0.0", 0, true},
		{"v0.9.8-0.20261109120000-abcdef123456", "v0.9.7", 1, true},
		{"dev", "v0.9.7", 0, false},
		{"v1.0", "v1.0.0", 0, false},
		{"v01.0.0", "v1.0.0", 0, false},
		{"v1.0.0-", "v1.0.0", 0, false},
	} {
		got, ok := Compare(tt.a, tt.b)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", tt.a, tt.b, got, ok, tt.want, tt.ok)
		}
	}
}

func TestGet(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	Version = "v1.2.3"
	if got := Get(); got != "v1.2.3" {
		t.Errorf("stamped: %q", got)
	}
	Version = Dev
	// A test binary is built from a working tree: no module version.
	if got := Get(); got != Dev {
		t.Errorf("unstamped: %q", got)
	}
}
