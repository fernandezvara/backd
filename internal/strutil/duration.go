package strutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses a Go duration ("12h", "90m") or a whole number of
// days ("30d").
func ParseDuration(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q: use a whole number of days (\"30d\") or Go syntax (\"12h\")", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use a whole number of days (\"30d\") or Go syntax (\"12h\")", v)
	}
	return d, nil
}
