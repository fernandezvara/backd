package client

import (
	"encoding/json"
	"testing"
)

func TestQueryValues(t *testing.T) {
	v, err := Query{
		Where:   Where{"status": "active", "price": Between(10, 20)},
		OrderBy: []string{"-_meta.created_at", "title"},
		Limit:   10, Skip: 5, After: "cur", Count: true, Deleted: "include", FileLinks: true,
		Expand: "author", Language: "es",
	}.values()
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("where") != `{"price":{"$between":[10,20]},"status":"active"}` {
		t.Errorf("where = %s", v.Get("where"))
	}
	want := map[string]string{"order_by": "-_meta.created_at,title", "limit": "10", "skip": "5", "after": "cur", "count": "true", "deleted": "include", "file_links": "true", "expand": "author", "language": "es"}
	for k, w := range want {
		if v.Get(k) != w {
			t.Errorf("%s = %q; want %q", k, v.Get(k), w)
		}
	}
	empty, _ := Query{}.values()
	if len(empty) != 0 {
		t.Errorf("the zero query sends %v", empty)
	}
}

func TestWhereAcceptsTextAndRawJSON(t *testing.T) {
	for _, w := range []any{`{"a":1}`, []byte(`{"a":1}`), json.RawMessage(`{"a":1}`), map[string]int{"a": 1}} {
		got, err := whereJSON(w)
		if err != nil || got != `{"a":1}` {
			t.Errorf("%T: %q %v", w, got, err)
		}
	}
	if _, err := whereJSON(make(chan int)); err == nil {
		t.Error("a value that can't be encoded is an error")
	}
}

func TestOperators(t *testing.T) {
	w := Or(Where{"a": Gt(1)}, And(Where{"b": In("x", "y")}, Where{"c": NotNull(), "d": IContains("z")}))
	b, _ := json.Marshal(w)
	if string(b) != `{"$or":[{"a":{"$gt":1}},{"$and":[{"b":{"$in":["x","y"]}},{"c":{"$notNull":true},"d":{"$icontains":"z"}}]}]}` {
		t.Errorf("%s", b)
	}
}

func TestExpand(t *testing.T) {
	got, err := Expand(Ref("author", "name", "avatar"), Ref("tags"), Ref("author.company"))
	if err != nil || got != "author(name,avatar),tags,author.company" {
		t.Errorf("%q %v", got, err)
	}
	for _, bad := range [][]ExpandPart{{Ref("")}, {Ref("a b")}, {Ref("1a")}, {Ref("a", "b c")}, {Ref("a", []string{}...)}, {{Name: "a", Fields: []string{}}}} {
		if _, err := Expand(bad...); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("MustExpand must panic on a bad name")
		}
	}()
	MustExpand(Ref("no good"))
}
