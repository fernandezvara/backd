package httpapi

import (
	"net/http"
	"testing"
)

// TestFunctionRateLimitPerUser: "capped" (rules_test.go's fixture) has
// rate_limit: { per: user, limit: 2, window: 1m }.
func TestFunctionRateLimitPerUser(t *testing.T) {
	f := newRulesFixture(t)
	const capped = "/v1/acme/app/_func/capped"

	if code, out := f.as(t, f.ada, "POST", capped, "{}"); code != http.StatusOK {
		t.Fatalf("call 1: %d %v", code, out)
	}
	if code, out := f.as(t, f.ada, "POST", capped, "{}"); code != http.StatusOK {
		t.Fatalf("call 2: %d %v", code, out)
	}
	rec, out := f.doH(t, "POST", capped, "{}", bearer(f.ada))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	if code, _ := errorOf(out); code != codeTooManyRequests {
		t.Errorf("code = %q", code)
	}

	// A different user has their own counter (per: user).
	if code, out := f.as(t, f.bob, "POST", capped, "{}"); code != http.StatusOK {
		t.Fatalf("a different user: %d %v", code, out)
	}
}

// TestFunctionRateLimitPerIP: "cappedip" has rate_limit: { per: ip,
// limit: 2, window: 1m }. Test requests all share the same synthetic
// RemoteAddr (httptest.NewRequest's default), so they count together.
func TestFunctionRateLimitPerIP(t *testing.T) {
	f := newRulesFixture(t)
	const cappedip = "/v1/acme/app/_func/cappedip"

	for range 2 {
		if code, out := f.as(t, f.ada, "POST", cappedip, "{}"); code != http.StatusOK {
			t.Fatalf("call: %d %v", code, out)
		}
	}
	// A different signed-in user, same client address, still shares the
	// per-IP counter.
	rec, out := f.doH(t, "POST", cappedip, "{}", bearer(f.bob))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3 (different user, same IP): %d %v", rec.Code, out)
	}
}

// A function without rate_limit is never throttled, however many times
// it's called.
func TestFunctionNoRateLimit(t *testing.T) {
	f := newRulesFixture(t)
	for range 5 {
		if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_func/typed", `{"n": 1}`); code != http.StatusOK {
			t.Fatalf("call: %d %v", code, out)
		}
	}
}
