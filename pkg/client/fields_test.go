package client

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// queryServer records the query of every request and answers a small document.
type queryServer struct {
	mu      sync.Mutex
	queries []url.Values
	headers []http.Header
}

func (s *queryServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.queries = append(s.queries, r.URL.Query())
	s.headers = append(s.headers, r.Header.Clone())
	s.mu.Unlock()
	if r.Method == "GET" && !strings.HasSuffix(r.URL.Path, "/p1") {
		_, _ = w.Write([]byte(`{"items":[{"id":"p1","title":"T"}],"limit":20,"skip":0,"has_more":false}`))
		return
	}
	if r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/restore") {
		w.WriteHeader(201)
	}
	_, _ = w.Write([]byte(`{"id":"p1","title":"T"}`))
}

func TestFieldsGoOnReadsAsOneParameter(t *testing.T) {
	var s queryServer
	posts := newTestClient(t, s.handler).DB("main").Collection("posts")
	one, err := posts.Get(bg, "p1", ReadOptions{Fields: []string{"title", "address.city"}})
	if err != nil || one["title"] != "T" {
		t.Fatalf("%v %v", one, err)
	}
	page, err := posts.List(bg, Query{Fields: []string{"title"}, Limit: 5})
	if err != nil || page.Items[0]["id"] != "p1" {
		t.Fatalf("%v %v", page, err)
	}
	if got := s.queries[0].Get("fields"); got != "title,address.city" {
		t.Errorf("get fields = %q", got)
	}
	if q := s.queries[1]; q.Get("fields") != "title" || q.Get("limit") != "5" {
		t.Errorf("list query = %v", q)
	}
}

func TestFieldsGoOnTheAnswersOfWrites(t *testing.T) {
	var s queryServer
	posts := newTestClient(t, s.handler).DB("main").Collection("posts")
	if _, err := posts.Create(bg, Doc{"title": "T"}, WithFields("title"), WithIdempotencyKey("k1")); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Replace(bg, "p1", Doc{"title": "T"}, WithFields("title"), WithIfMatch(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Patch(bg, "p1", Doc{"title": "U"}, WithFields("_meta.version")); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Restore(bg, "p1", WithFields("title")); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"title", "title", "_meta.version", "title"} {
		if got := s.queries[i].Get("fields"); got != want {
			t.Errorf("request %d: fields = %q, want %q", i, got, want)
		}
	}
	if s.headers[0].Get("Idempotency-Key") != "k1" || s.headers[1].Get("If-Match") != `"1"` {
		t.Errorf("the other options were lost: %v %v", s.headers[0], s.headers[1])
	}
}

func TestWithoutFieldsNothingIsAdded(t *testing.T) {
	var s queryServer
	posts := newTestClient(t, s.handler).DB("main").Collection("posts")
	if _, err := posts.Get(bg, "p1", ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if s.queries[0].Has("fields") {
		t.Errorf("query = %v", s.queries[0])
	}
}

func TestFieldsMustBeNonEmptyPaths(t *testing.T) {
	var s queryServer
	posts := newTestClient(t, s.handler).DB("main").Collection("posts")
	if _, err := posts.Get(bg, "p1", ReadOptions{Fields: []string{"title", " "}}); err == nil || !strings.Contains(err.Error(), "fields") {
		t.Errorf("get: %v", err)
	}
	if _, err := posts.List(bg, Query{Fields: []string{""}}); err == nil {
		t.Error("list accepted an empty path")
	}
	if _, err := posts.Patch(bg, "p1", Doc{}, WithFields()); err == nil {
		t.Error("WithFields() with no path was accepted")
	}
	if len(s.queries) != 0 {
		t.Errorf("%d requests were sent", len(s.queries))
	}
}
