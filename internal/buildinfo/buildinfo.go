// Package buildinfo is the version backd was built as, the same for both binaries.
package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// Version is set at build time (-ldflags "-X
// github.com/fernandezvara/backd/internal/buildinfo.Version=v1.2.3") by releases and the
// Dockerfiles. Read it with Get, which falls back to the module version.
var Version = "dev"

// Dev is the version of a build from a working tree.
const Dev = "dev"

// Get is the version of this binary: the release-stamped one, else the module version a `go
// install …@vX.Y.Z` records, else "dev" (a build from a working tree, "(devel)").
func Get() string {
	if Version != "" && Version != Dev {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Dev
}

// Commit is the VCS revision the binary was built from ("unknown" when the
// build has none, such as a container build without .git).
func Commit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value[:min(len(s.Value), 12)]
			}
		}
	}
	return "unknown"
}

// Compare orders two semantic versions (a leading "v" is optional) by semver precedence: -1,
// 0 or +1, and ok false when either isn't one.
func Compare(a, b string) (int, bool) {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		if pa.core[i] != pb.core[i] {
			if pa.core[i] < pb.core[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return comparePre(pa.pre, pb.pre), true
}

type semver struct {
	core [3]uint64
	pre  []string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+") // build metadata has no precedence
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	var s semver
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || (len(p) > 1 && p[0] == '0') {
			return s, false
		}
		s.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return s, false
		}
		s.pre = strings.Split(pre, ".")
		for _, id := range s.pre {
			if id == "" {
				return s, false
			}
		}
	}
	return s, true
}

// comparePre orders pre-release identifiers: none ranks above any; numeric identifiers are
// compared as numbers and rank below alphanumeric ones; a shorter list ranks lower when equal
// so far.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		na, ea := strconv.ParseUint(a[i], 10, 64)
		nb, eb := strconv.ParseUint(b[i], 10, 64)
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
