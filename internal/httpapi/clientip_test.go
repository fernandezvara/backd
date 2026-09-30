package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	nets := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	tests := []struct {
		name, remote, xff string
		trusted           []netip.Prefix
		want              string
	}{
		{"direct", "203.0.113.9:1234", "", nets, "203.0.113.9"},
		{"untrusted peer's header is ignored", "203.0.113.9:1234", "198.51.100.1", nets, "203.0.113.9"},
		{"no trusted proxies configured", "10.0.0.1:1234", "198.51.100.1", nil, "10.0.0.1"},
		{"one proxy", "10.0.0.1:1234", "198.51.100.1", nets, "198.51.100.1"},
		{"client-supplied entries left of the real client are ignored", "10.0.0.1:1234", "1.2.3.4, 198.51.100.1", nets, "198.51.100.1"},
		{"chain of proxies", "10.0.0.1:1234", "198.51.100.1, 10.1.1.1, 10.2.2.2", nets, "198.51.100.1"},
		{"garbage stops the walk", "10.0.0.1:1234", "198.51.100.1, junk", nets, "10.0.0.1"},
		{"only proxies", "10.0.0.1:1234", "10.3.3.3", nets, "10.3.3.3"},
		{"ipv6", "[fd00::1]:443", "2001:db8::7", nets, "2001:db8::7"},
		{"mapped ipv4", "[::ffff:203.0.113.9]:1", "", nets, "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := withTrustedProxies(tt.trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = clientIP(r) }))
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoginThrottledOverHTTP(t *testing.T) {
	f := newAuthFixture(t)
	f.signup(t, "ada@example.com")
	for range 5 {
		if rec, _ := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!0"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed login: %d", rec.Code)
		}
	}
	rec, out := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusTooManyRequests || errCode(out) != "too_many_requests" || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("throttled login: %d %v Retry-After=%q", rec.Code, out, rec.Header().Get("Retry-After"))
	}
	if strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Error("email echoed in the throttling response")
	}
}
