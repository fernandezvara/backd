package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
)

func TestListSendsTheQueryAndReadsThePage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/r/main/posts" || r.Method != "GET" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		q := r.URL.Query()
		if q.Get("where") != `{"status":"active"}` || q.Get("order_by") != "-_meta.created_at" || q.Get("limit") != "2" || q.Get("team") != "" {
			t.Errorf("query = %v", q)
		}
		io.WriteString(w, `{"items":[{"id":"a","title":"A","_meta":{"version":3,"created_at":"x","updated_at":"y","score":1.5}},{"id":"b","title":"B","_meta":{}}],"limit":2,"skip":0,"has_more":true,"next_cursor":"cur","total":9}`)
	})
	page, err := c.DB("main").Collection("posts").List(bg, Query{Where: Where{"status": "active"}, OrderBy: []string{"-_meta.created_at"}, Limit: 2})
	if err != nil || len(page.Items) != 2 || !page.HasMore || page.NextCursor != "cur" || *page.Total != 9 {
		t.Fatalf("%v %+v", err, page)
	}
	if d := page.Items[0]; d.ID() != "a" || d["title"] != "A" || d.Meta().Version != 3 || *d.Meta().Score != 1.5 {
		t.Errorf("%+v", d)
	}
}

type post struct {
	Base
	Title string `json:"title"`
}

func TestTypedDocuments(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			io.WriteString(w, `{"id":"p1","title":"Hi","_meta":{"version":2,"created_at":"c"},"_expanded":{"author":{"name":"Ada"}}}`)
		case "POST":
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"p2","title":"New","_meta":{"version":1}}`)
		}
	})
	posts := As[post](c.DB("main").Collection("posts"))
	got, err := posts.Get(bg, "p1", ReadOptions{Expand: "author"})
	if err != nil || got.ID != "p1" || got.Title != "Hi" || got.Meta.Version != 2 || got.Expanded["author"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("%v %+v", err, got)
	}
	created, err := posts.Create(bg, post{Title: "New"})
	if err != nil || created.ID != "p2" {
		t.Fatalf("%v %+v", err, created)
	}
}

func TestIterateFollowsCursors(t *testing.T) {
	var calls []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, q.Get("after")+"|"+q.Get("skip")+"|"+q.Get("limit"))
		switch q.Get("after") {
		case "":
			io.WriteString(w, `{"items":[{"id":"1"},{"id":"2"}],"has_more":true,"next_cursor":"c1"}`)
		case "c1":
			io.WriteString(w, `{"items":[{"id":"3"}],"has_more":false}`)
		}
	})
	var ids []string
	for d, err := range c.DB("main").Collection("posts").Iterate(bg, Query{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID())
	}
	if fmt.Sprint(ids) != "[1 2 3]" || fmt.Sprint(calls) != "[||2 c1||2]" {
		t.Errorf("ids %v calls %v", ids, calls)
	}
}

func TestIterateSkipsWhenThereIsNoCursor(t *testing.T) {
	var skips []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		s, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		skips = append(skips, strconv.Itoa(s))
		fmt.Fprintf(w, `{"items":[{"id":"%d"}],"has_more":%v}`, s, s < 2)
	})
	n := 0
	for _, err := range c.DB("main").Collection("posts").Iterate(bg, Query{Limit: 1, OrderBy: []string{"_relevance"}}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 3 || fmt.Sprint(skips) != "[0 1 2]" {
		t.Errorf("n %d skips %v", n, skips)
	}
}

func TestIterateStopsOnErrorsAndOnBreak(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"forbidden","message":"no"}}`, http.StatusForbidden)
	})
	for _, err := range c.DB("main").Collection("posts").Iterate(bg, Query{}) {
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("err = %v", err)
		}
	}
	calls := 0
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"items":[{"id":"1"},{"id":"2"}],"has_more":true,"next_cursor":"c"}`)
	})
	for range c2.DB("main").Collection("posts").Iterate(bg, Query{}) {
		break
	}
	if calls != 1 {
		t.Errorf("a break read %d pages", calls)
	}
}

func TestIterateNoCursorAfterIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"items":[],"has_more":true}`) })
	var last error
	for _, err := range c.DB("main").Collection("posts").Iterate(bg, Query{After: "x"}) {
		last = err
	}
	if last == nil {
		t.Error("expected an error")
	}
}

func TestWrites(t *testing.T) {
	type seen struct{ method, path, ct, ifMatch, idem, body, query string }
	var got []seen
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("If-Match"), r.Header.Get("Idempotency-Key"), string(b), r.URL.RawQuery})
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, `{"id":"x","_meta":{"version":2}}`)
	})
	posts := c.DB("main").Collection("posts").InTeam("docs")
	posts.Create(bg, Doc{"title": "t"}, WithIdempotencyKey("k1"), WithOrg("acme"))
	posts.Replace(bg, "x", Doc{"title": "u"}, WithIfMatch(3))
	posts.Patch(bg, "x", Doc{"title": nil}, WithIfMatch(`"4"`))
	posts.Delete(bg, "x", WithPurge())
	posts.Restore(bg, "x")
	want := []seen{
		{"POST", "/v1/r/main/posts", "application/json", "", "k1", `{"title":"t"}`, "org=acme&team=docs"},
		{"PUT", "/v1/r/main/posts/x", "application/json", `"3"`, "", `{"title":"u"}`, ""},
		{"PATCH", "/v1/r/main/posts/x", "application/merge-patch+json", `"4"`, "", `{"title":null}`, ""},
		{"DELETE", "/v1/r/main/posts/x", "", "", "", "", "purge=true"},
		{"POST", "/v1/r/main/posts/x/restore", "", "", "", "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d requests", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestVersionMismatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"version_mismatch","message":"moved on"}}`, http.StatusPreconditionFailed)
	})
	_, err := c.DB("main").Collection("posts").Replace(bg, "x", Doc{}, WithIfMatch(1))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.DB("main").Collection("posts").Replace(bg, "x", Doc{}, WithIfMatch(1.5)); err == nil {
		t.Error("an If-Match that is not a version")
	}
}

func TestSearchOrdersByRelevance(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var w2 map[string]any
		json.Unmarshal([]byte(q.Get("where")), &w2)
		if w2["$search"] != "blue shoes" || w2["status"] != "active" || q.Get("order_by") != "_relevance" || q.Get("after") != "" || q.Get("language") != "en" {
			t.Errorf("query = %v", q)
		}
		io.WriteString(w, `{"items":[]}`)
	})
	posts := c.DB("main").Collection("posts")
	if _, err := posts.Search(bg, "blue shoes", Query{Where: Where{"status": "active"}, OrderBy: []string{"title"}, After: "x", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Search(bg, "blue shoes", Query{Where: `{"status":"active"}`, Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Search(bg, "  ", Query{}); err == nil {
		t.Error("an empty search")
	}
}

func TestTeamScopesListsAndAggregates(t *testing.T) {
	var qs []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		qs = append(qs, r.URL.Path+"?"+r.URL.Query().Encode())
		io.WriteString(w, `{"items":[],"groups":[{"key":{"status":"a"},"count":2,"sum_total":"7"}],"total_groups":1}`)
	})
	posts := c.DB("main").Collection("posts").InTeam("docs")
	posts.List(bg, Query{})
	res, err := posts.Aggregate(bg, AggregateParams{GroupBy: []string{"status"}, Metrics: []string{"count", "sum:total"}, Where: Where{"x": 1}, OrderBy: []string{"-count"}, Limit: 5, TZ: "Europe/Madrid"})
	if err != nil {
		t.Fatal(err)
	}
	if qs[0] != "/v1/r/main/posts?team=docs" || qs[1] != "/v1/r/main/posts/_aggregate?group_by=status&limit=5&metrics=count%2Csum%3Atotal&order_by=-count&team=docs&tz=Europe%2FMadrid&where=%7B%22x%22%3A1%7D" {
		t.Errorf("queries %q", qs)
	}
	g := res.Groups[0]
	if cnt, _ := g.Count(); res.TotalGroups != 1 || g.Key["status"] != "a" || cnt != 2 || g.Values["sum_total"] != "7" {
		t.Errorf("%+v", res)
	}
	b, _ := json.Marshal(g)
	if string(b) != `{"count":2,"key":{"status":"a"},"sum_total":"7"}` {
		t.Errorf("marshal = %s", b)
	}
}

func TestBatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operations []map[string]any `json:"operations"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/r/main/_batch" || len(body.Operations) != 3 {
			t.Errorf("%s %+v", r.URL.Path, body)
		}
		o := body.Operations
		if o[0]["op"] != "create" || o[0]["document"].(map[string]any)["n"] != float64(1) || o[0]["if_match"] != nil || o[0]["id"] != nil {
			t.Errorf("op 0 = %v", o[0])
		}
		if o[1]["if_match"] != `"2"` || o[1]["patch"] == nil || o[2]["id"] != "z" {
			t.Errorf("ops = %v", o)
		}
		io.WriteString(w, `{"results":[{"id":"n","n":1},{"id":"y"},{"id":"z"}]}`)
	})
	res, err := c.DB("main").Batch(bg, []BatchOperation{
		{Op: "create", Collection: "a", Document: map[string]any{"n": 1}},
		{Op: "patch", Collection: "a", ID: "y", Patch: map[string]any{"n": 2}, IfMatch: 2},
		{Op: "delete", Collection: "a", ID: "z"},
	}, WithIdempotencyKey("b1"))
	if err != nil || len(res) != 3 || res[0].ID() != "n" {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := c.DB("main").Batch(bg, []BatchOperation{{Op: "patch", IfMatch: 1.5}}); err == nil {
		t.Error("a bad If-Match")
	}
}

func TestIfMatchValue(t *testing.T) {
	for in, want := range map[any]string{3: `"3"`, int64(4): `"4"`, uint64(5): `"5"`, `W/"x"`: `W/"x"`} {
		if got, err := IfMatchValue(in); err != nil || got != want {
			t.Errorf("%v = %q %v", in, got, err)
		}
	}
}
