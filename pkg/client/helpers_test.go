package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient starts a server running h and returns a client for realm "r" that talks to it.
func newTestClient(t *testing.T, h http.HandlerFunc, mod ...func(*Options)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	o := Options{URL: srv.URL, Realm: "r"}
	for _, m := range mod {
		m(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

var bg = context.Background()
