package cmdutil

import (
	"fmt"
	"time"

	"github.com/fernandezvara/backd/internal/strutil"
)

// SinceParam turns --since (an RFC 3339 time, or a duration back from now
// such as 24h or 7d) into the time the API takes.
func SinceParam(v string, now time.Time) (string, error) {
	if d, err := strutil.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d).UTC().Format(time.RFC3339), nil
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return v, nil
	}
	return "", fmt.Errorf("--since: want an RFC 3339 time or a duration such as 24h or 7d, got %q", v)
}
