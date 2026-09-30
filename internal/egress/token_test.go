package egress

import (
	"testing"
	"time"
)

const testKey = "01234567890123456789012345678901"

func TestSignVerifyRoundTrip(t *testing.T) {
	key := []byte(testKey)
	c := Claims{Hosts: []string{"api.stripe.com", "backd:8081"}, Expires: time.Now().Add(time.Minute)}
	tok := Sign(key, c)
	got, err := Verify(key, tok, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 2 || got.Hosts[0] != "api.stripe.com" || got.Hosts[1] != "backd:8081" {
		t.Fatalf("got %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	key := []byte(testKey)
	other := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	tok := Sign(key, Claims{Hosts: []string{"a"}, Expires: time.Now().Add(time.Minute)})

	cases := []struct {
		name  string
		token string
		key   []byte
		now   time.Time
	}{
		{"wrong key", tok, other, time.Now()},
		{"tampered", tok + "x", key, time.Now()},
		{"expired", tok, key, time.Now().Add(time.Hour)},
		{"garbage", "not-a-token", key, time.Now()},
		{"empty", "", key, time.Now()},
		{"no prefix", "bdf_" + tok[len(TokenPrefix):], key, time.Now()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Verify(tc.key, tc.token, tc.now); err == nil {
				t.Fatal("accepted, want a rejection")
			}
		})
	}
}

func TestClaimsAllows(t *testing.T) {
	c := Claims{Hosts: []string{"api.stripe.com", "backd:8081", "169.254.169.254:80"}}
	cases := []struct {
		host string
		want bool
	}{
		{"api.stripe.com:443", true}, // bare host: any port
		{"api.stripe.com:80", true},
		{"backd:8081", true},
		{"backd:9999", false}, // host:port entry: exact port only
		{"evil.example:443", false},
		{"169.254.169.254:80", true},
		{"169.254.169.254:81", false},
	}
	for _, tc := range cases {
		if got := c.Allows(tc.host); got != tc.want {
			t.Errorf("Allows(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
