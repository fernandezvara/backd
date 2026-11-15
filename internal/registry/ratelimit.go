package registry

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/ratelimit"
)

// The request classes of the built-in rate limiter (design/rate-limiting.md §3).
const (
	RateAnonymous = "anonymous" // a request without credentials, by client address
	RateRead      = "read"      // GET and HEAD, by principal
	RateWrite     = "write"     // everything else on the data routes, by principal
	RateAuth      = "auth"      // /_auth, before the caller is known, by client address
	RateFiles     = "files"     // file transfers, by principal
	RateAdmin     = "admin"     // /_admin, by principal
)

// RateClasses lists the classes in the order the documentation and the start-up log give them.
var RateClasses = []string{RateAnonymous, RateRead, RateWrite, RateAuth, RateFiles, RateAdmin}

// DefaultRates are the limits of each class when nothing sets them: generous enough that a normal
// app never meets them, low enough to stop a loop in a script.
var DefaultRates = map[string]ratelimit.Rate{
	RateAnonymous: {Limit: 120, Window: time.Minute},
	RateRead:      {Limit: 600, Window: time.Minute},
	RateWrite:     {Limit: 120, Window: time.Minute},
	RateAuth:      {Limit: 30, Window: time.Minute},
	RateFiles:     {Limit: 120, Window: time.Minute},
	RateAdmin:     {Limit: 300, Window: time.Minute},
}

// IsRateClass reports whether name is a request class.
func IsRateClass(name string) bool { return slices.Contains(RateClasses, name) }

// ParseRate reads "<requests>/<window>", such as "600/1m" or "30/10s". "none" is no limit (the zero
// Rate). Requests is a positive integer and the window a duration of at least a second.
func ParseRate(s string) (ratelimit.Rate, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "none") {
		return ratelimit.Rate{}, nil
	}
	n, window, ok := strings.Cut(s, "/")
	if !ok {
		return ratelimit.Rate{}, fmt.Errorf("%q is not <requests>/<window>, such as 600/1m, or none", s)
	}
	limit, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || limit < 1 {
		return ratelimit.Rate{}, fmt.Errorf("%q: the number of requests must be a positive integer", s)
	}
	d, err := time.ParseDuration(strings.TrimSpace(window))
	if err != nil || d < time.Second || d > 24*time.Hour {
		return ratelimit.Rate{}, fmt.Errorf("%q: the window must be a duration from 1s to 24h, such as 1m", s)
	}
	return ratelimit.Rate{Limit: limit, Window: d}, nil
}

// FormatRate is a Rate as ParseRate reads it.
func FormatRate(r ratelimit.Rate) string {
	if r.Unlimited() {
		return "none"
	}
	return strconv.Itoa(r.Limit) + "/" + r.Window.String()
}

// RateSettings are the instance's limits (BACKD_RATE_LIMIT*): whether the limiter is on, and the
// rate of each class. A realm's `rate_limit:` and an API key's limit refine them.
type RateSettings struct {
	Enabled bool
	Rates   map[string]ratelimit.Rate
}

// DefaultRateSettings is the limiter on, with the default rates.
func DefaultRateSettings() RateSettings {
	return RateSettings{Enabled: true, Rates: maps.Clone(DefaultRates)}
}

// RateFor is the rate of class for a realm: the realm's own when it sets one, else the instance's.
func (s RateSettings) RateFor(realm *RealmSettings, class string) ratelimit.Rate {
	if realm != nil {
		if r, ok := realm.RateLimits[class]; ok {
			return r
		}
	}
	if r, ok := s.Rates[class]; ok {
		return r
	}
	return DefaultRates[class]
}
