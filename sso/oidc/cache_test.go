package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A key endpoint that does not answer must not hold up the sign-ins of everybody else: an
// organization's own server is one of the things this client reaches.
func TestASlowKeyEndpointDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"keys":[]}`)) }))
	defer fast.Close()

	v := &Verifier{HTTP: http.DefaultClient}
	go func() { _, _ = v.keys(context.Background(), slow.URL, false) }()
	time.Sleep(100 * time.Millisecond) // the slow request is in flight

	done := make(chan error, 1)
	go func() { _, err := v.keys(context.Background(), fast.URL, false); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a slow key endpoint blocked another provider's keys")
	}
}

func TestCachesAreBounded(t *testing.T) {
	now := time.Now()
	m := map[string]time.Time{}
	for i := range maxCached {
		m[string(rune('a'+i%26))+string(rune('A'+i/26))] = now.Add(time.Duration(i+1) * time.Minute)
	}
	evict(m, now, func(at time.Time) time.Time { return at })
	if len(m) >= maxCached {
		t.Fatalf("no room made: %d entries", len(m))
	}
	// Expired entries go first.
	m2 := map[string]time.Time{"old": now.Add(-time.Hour), "new": now.Add(time.Hour)}
	evict(m2, now, func(at time.Time) time.Time { return at })
	if _, ok := m2["old"]; ok || len(m2) != 1 {
		t.Fatalf("%v", m2)
	}
}
