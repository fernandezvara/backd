package client_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/fernandezvara/backd/pkg/client"
)

// demo is a small stand-in for a backd server, so the examples run.
func demo() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/shelf/_auth/login":
			fmt.Fprint(w, `{"token":"bds_demo","session_id":"s1","expires_at":"2030-01-01T00:00:00Z","user":{"id":"u1","email":"ada@example.com"}}`)
		case r.URL.Path == "/v1/shelf/main/assets" && r.URL.Query().Get("after") == "":
			fmt.Fprint(w, `{"items":[{"id":"a1","title":"Docs","_meta":{"version":1}},{"id":"a2","title":"Notes","_meta":{"version":1}}],"has_more":true,"next_cursor":"c1"}`)
		case r.URL.Path == "/v1/shelf/main/assets":
			fmt.Fprint(w, `{"items":[{"id":"a3","title":"Links","_meta":{"version":1}}],"has_more":false}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"no such document","request_id":"rq1"}}`)
		}
	}))
}

// A client talks to one realm: sign in once, and the token goes with every call.
func Example() {
	srv := demo()
	defer srv.Close()
	ctx := context.Background()

	c, err := client.New(client.Options{URL: srv.URL, Realm: "shelf"})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	session, err := c.Auth().Login(ctx, client.SignInInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"})
	if err != nil {
		panic(err)
	}
	fmt.Println("signed in as", session.User.Email)

	page, err := c.DB("main").Collection("assets").List(ctx, client.Query{Limit: 2})
	if err != nil {
		panic(err)
	}
	for _, doc := range page.Items {
		fmt.Println(doc.ID(), doc["title"])
	}
	// Output:
	// signed in as ada@example.com
	// a1 Docs
	// a2 Notes
}

// Iterate reads everything a query matches, page after page, following the cursors.
func ExampleTyped_Iterate() {
	srv := demo()
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL, Realm: "shelf"})
	defer c.Close()

	assets := c.DB("main").Collection("assets")
	for doc, err := range assets.Iterate(context.Background(), client.Query{Limit: 2}) {
		if err != nil {
			panic(err)
		}
		fmt.Println(doc.ID())
	}
	// Output:
	// a1
	// a2
	// a3
}

// Failures are *client.Error values: test their class with errors.Is.
func ExampleError() {
	srv := demo()
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL, Realm: "shelf"})
	defer c.Close()

	_, err := c.DB("main").Collection("assets").Get(context.Background(), "nope", client.ReadOptions{})
	var e *client.Error
	if errors.Is(err, client.ErrNotFound) && errors.As(err, &e) {
		fmt.Printf("%d %s: %s (request %s)\n", e.Status, e.Code, e.Message, e.RequestID)
	}
	// Output:
	// 404 not_found: no such document (request rq1)
}

// Where is a filter in the query language: field names to values, or to operators.
func ExampleWhere() {
	w := client.Or(
		client.Where{"status": "published", "views": client.Gte(100)},
		client.Where{"tags": client.In("go", "backd")},
	)
	q := client.Query{Where: w, OrderBy: []string{"-_meta.created_at"}, Limit: 20}
	fmt.Println(q.OrderBy, q.Limit)
	// Output: [-_meta.created_at] 20
}

// Sizes follow the one grammar of the server: a number and a case-sensitive unit.
func ExampleParseSize() {
	n, err := client.ParseSize("1.5MiB", client.SizeBoth, false)
	fmt.Println(n, err)
	fmt.Println(client.FormatSize(n, client.SizeBinary))
	_, err = client.ParseSize("1.5mib", client.SizeBoth, false)
	fmt.Println(strings.HasPrefix(err.Error(), `invalid size "1.5mib"`))
	// Output:
	// 1572864 <nil>
	// 1.5 MiB
	// true
}

// Do is the primitive every call is made with, for endpoints the client has no method for.
func ExampleClient_Do() {
	srv := demo()
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL, Realm: "shelf"})
	defer c.Close()

	var page struct {
		Items []struct{ ID string }
	}
	_, err := c.Do(context.Background(), "GET", []string{"main", "assets"}, nil, &page)
	fmt.Println(len(page.Items), err)
	// Output: 2 <nil>
}
