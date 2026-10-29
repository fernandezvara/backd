package strutil

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var sizePattern = regexp.MustCompile(`^([0-9]+)(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)$`)

var sizeUnits = map[string]int64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}

// ParseSize parses a size such as 128MB (decimal) or 1MiB (binary).
func ParseSize(v string) (int64, error) {
	m := sizePattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q: want a whole number and a unit (B, KB, MB, GB, TB, KiB, MiB, GiB, TiB), such as 128MB or 1MiB", v)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	unit := sizeUnits[m[2]]
	if err != nil || n > math.MaxInt64/unit {
		return 0, fmt.Errorf("invalid size %q: too large", v)
	}
	return n * unit, nil
}
