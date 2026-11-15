package client

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// whoServer answers a post with the user it was asked as, with the same ETag for everyone.
type whoServer struct {
	mu      sync.Mutex
	headers []http.Header
}

func (s *whoServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.headers = append(s.headers, r.Header.Clone())
	s.mu.Unlock()
	who := r.Header.Get("X-Backd-On-Behalf-Of")
	if who == "" {
		who = "key"
	}
	w.Header().Set("ETag", `"1"`)
	_, _ = w.Write([]byte(`{"id":"p1","title":"seen by ` + who + `"}`))
}

func withKey(o *Options) { o.APIKey = "bdk_secret" }

func TestAsUserSendsTheHeaderAndKeepsTheKey(t *testing.T) {
	var s whoServer
	key := newTestClient(t, s.handler, withKey)
	ada, err := key.AsUser("u_ada")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ada.DB("main").Collection("posts").Get(bg, "p1", ReadOptions{})
	if err != nil || got["title"] != "seen by u_ada" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := key.DB("main").Collection("posts").Get(bg, "p1", ReadOptions{}); got["title"] != "seen by key" {
		t.Errorf("the client it came from is changed: %v", got)
	}
	if h := s.headers[0]; h.Get("X-Backd-On-Behalf-Of") != "u_ada" || h.Get("Authorization") != "Bearer bdk_secret" {
		t.Errorf("headers = %v", h)
	}
	if ada.OnBehalfOf() != "u_ada" || key.OnBehalfOf() != "" {
		t.Errorf("OnBehalfOf = %q / %q", ada.OnBehalfOf(), key.OnBehalfOf())
	}
}

func TestEachActingClientHasACacheOfItsOwn(t *testing.T) {
	var s whoServer
	key := newTestClient(t, s.handler, withKey)
	ada, _ := key.AsUser("u_ada")
	bob, _ := key.AsUser("u_bob")
	if _, err := ada.DB("main").Collection("posts").Get(bg, "p1", ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := bob.DB("main").Collection("posts").Get(bg, "p1", ReadOptions{})
	if err != nil || got["title"] != "seen by u_bob" {
		t.Fatalf("%v %v", got, err)
	}
	if s.headers[1].Get("If-None-Match") != "" {
		t.Error("bob revalidated with ada's tag")
	}
	if a, b, k := ada.CacheStats().Entries, bob.CacheStats().Entries, key.CacheStats().Entries; a != 1 || b != 1 || k != 0 {
		t.Errorf("entries: ada %d, bob %d, key %d", a, b, k)
	}
}

func TestAsUserNeedsAKeyAnIDAndCannotBeChained(t *testing.T) {
	var s whoServer
	if _, err := newTestClient(t, s.handler).AsUser("u1"); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("without a key: %v", err)
	}
	key := newTestClient(t, s.handler, withKey)
	ada, err := key.AsUser("u_ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ada.AsUser("u_bob"); err == nil || !strings.Contains(err.Error(), "already acts as a user") {
		t.Errorf("chained: %v", err)
	}
	if _, err := key.AsUser("  "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("empty id: %v", err)
	}
}

func TestAnActingClientRefusesTheAccountRoutesBeforeSending(t *testing.T) {
	var s whoServer
	ada, _ := newTestClient(t, s.handler, withKey).AsUser("u_ada")
	if _, err := ada.Auth().Me(bg); err == nil || !strings.Contains(err.Error(), "cannot use the admin API or the account routes") {
		t.Errorf("Me: %v", err)
	}
	if len(s.headers) != 0 {
		t.Errorf("%d requests were sent", len(s.headers))
	}
}
