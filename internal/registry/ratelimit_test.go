package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/ratelimit"
)

func TestParseRate(t *testing.T) {
	good := map[string]ratelimit.Rate{
		"600/1m":   {Limit: 600, Window: time.Minute},
		" 30/10s ": {Limit: 30, Window: 10 * time.Second},
		"5/24h":    {Limit: 5, Window: 24 * time.Hour},
		"none":     {},
		"NONE":     {},
	}
	for in, want := range good {
		got, err := ParseRate(in)
		if err != nil || got != want {
			t.Errorf("ParseRate(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "600", "0/1m", "-3/1m", "x/1m", "600/forever", "600/500ms", "600/25h", "600/"} {
		if _, err := ParseRate(in); err == nil {
			t.Errorf("ParseRate(%q) was accepted", in)
		}
	}
	if FormatRate(ratelimit.Rate{Limit: 600, Window: time.Minute}) != "600/1m0s" || FormatRate(ratelimit.Rate{}) != "none" {
		t.Error("FormatRate")
	}
	if r, err := ParseRate(FormatRate(ratelimit.Rate{Limit: 600, Window: time.Minute})); err != nil || r.Limit != 600 {
		t.Errorf("FormatRate does not round-trip: %v %v", r, err)
	}
}

func TestRealmRateLimitOverridesTheInstance(t *testing.T) {
	s, errs := parseRealmSettings([]byte(authBase + "rate_limit:\n  read: 1200/1m\n  write: none\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	inst := DefaultRateSettings()
	if r := inst.RateFor(&s, RateRead); r.Limit != 1200 {
		t.Errorf("read = %v", r)
	}
	if r := inst.RateFor(&s, RateWrite); !r.Unlimited() {
		t.Errorf("write = %v: the realm set none", r)
	}
	if r := inst.RateFor(&s, RateAuth); r != DefaultRates[RateAuth] {
		t.Errorf("auth = %v: the realm sets nothing for it", r)
	}
	if r := inst.RateFor(nil, RateRead); r != DefaultRates[RateRead] {
		t.Errorf("no realm: %v", r)
	}
}

func TestRealmRateLimitErrors(t *testing.T) {
	for in, want := range map[string]string{
		"rate_limit:\n  reads: 10/1m":   `rate_limit.reads: not a request class; the classes are anonymous, read, write, auth, files, admin`,
		"rate_limit:\n  read: lots":     `rate_limit.read: "lots" is not <requests>/<window>`,
		"rate_limit:\n  read: 0/1m":     `rate_limit.read: "0/1m": the number of requests must be a positive integer`,
		"rate_limit:\n  read: 10/100ms": "the window must be a duration from 1s to 24h",
	} {
		_, errs := parseRealmSettings([]byte(authBase + in + "\n"))
		var got []string
		for _, e := range errs {
			got = append(got, e.Error())
		}
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("%q: %v, want %q", in, got, want)
		}
	}
}
