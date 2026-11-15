package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"
)

const itemsPath = "/v1/shop/orders/items"

func keysOf(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func TestFieldsSelectWhatAReadReturns(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", itemsPath, `{"name": "Widget", "age": 3, "price": 2.5, "address": {"city": "Madrid", "zip": "28001"}}`)
	id := created["id"].(string)

	rec, got := f.do(t, "GET", itemsPath+"/"+id+"?fields="+url.QueryEscape("name,address.city"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %v", rec.Code, got)
	}
	if want := []string{"address", "id", "name"}; !reflect.DeepEqual(keysOf(got), want) {
		t.Errorf("keys = %v, want %v", keysOf(got), want)
	}
	if addr := got["address"].(map[string]any); !reflect.DeepEqual(keysOf(addr), []string{"city"}) {
		t.Errorf("address = %v", addr)
	}

	_, got = f.do(t, "GET", itemsPath+"/"+id+"?fields="+url.QueryEscape("name,_meta.version"), "")
	if meta, _ := got["_meta"].(map[string]any); !reflect.DeepEqual(keysOf(meta), []string{"version"}) || got["age"] != nil {
		t.Errorf("_meta.version: %v", got)
	}

	// Without fields nothing changes.
	_, full := f.do(t, "GET", itemsPath+"/"+id, "")
	if full["age"] != float64(3) || full["_meta"] == nil {
		t.Errorf("full = %v", full)
	}
}

func TestFieldsOnListsKeepPagingWhole(t *testing.T) {
	f := newFixture(t)
	for _, age := range []int{3, 1, 2} {
		f.do(t, "POST", itemsPath, `{"name": "n", "age": `+string(rune('0'+age))+`}`)
	}
	// Ordered by age, which is not selected: the cursor is built from the stored document.
	q := "?fields=name&order_by=age&limit=1"
	rec, page := f.do(t, "GET", itemsPath+q, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %v", rec.Code, page)
	}
	items := page["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 || page["next_cursor"] == nil {
		t.Fatalf("page 1: %d %v", rec.Code, page)
	}
	if first := items[0].(map[string]any); first["age"] != nil || first["name"] != "n" || first["id"] == nil {
		t.Errorf("item = %v", first)
	}
	rec, page2 := f.do(t, "GET", itemsPath+q+"&after="+url.QueryEscape(page["next_cursor"].(string)), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2: %d %v", rec.Code, page2)
	}
	if page2["items"].([]any)[0].(map[string]any)["id"] == items[0].(map[string]any)["id"] {
		t.Error("the second page repeated the first item")
	}
}

func TestFieldsChangeTheValidator(t *testing.T) {
	f := newFixture(t)
	_, created := f.do(t, "POST", itemsPath, `{"name": "Widget", "age": 3}`)
	path := itemsPath + "/" + created["id"].(string)

	rec, _ := f.doH(t, "GET", path, "", nil)
	fullTag := rec.Header().Get("ETag")
	rec, _ = f.doH(t, "GET", path+"?fields=name", "", nil)
	partialTag := rec.Header().Get("ETag")
	if fullTag == "" || partialTag == "" || fullTag == partialTag {
		t.Fatalf("tags: full %q, partial %q", fullTag, partialTag)
	}
	// The partial tag revalidates a partial read, never a full one.
	if rec, _ := f.doH(t, "GET", path+"?fields=name", "", map[string]string{"If-None-Match": partialTag}); rec.Code != http.StatusNotModified {
		t.Errorf("same fields: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", path, "", map[string]string{"If-None-Match": partialTag}); rec.Code != http.StatusOK {
		t.Errorf("a full read matched a partial tag: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", path+"?fields=name,age", "", map[string]string{"If-None-Match": partialTag}); rec.Code != http.StatusOK {
		t.Errorf("other fields matched: %d", rec.Code)
	}
}

func TestFieldsOnWritesShapeTheAnswerOnly(t *testing.T) {
	f := newFixture(t)
	rec, got := f.do(t, "POST", itemsPath+"?fields=name", `{"name": "Widget", "age": 3}`)
	if rec.Code != http.StatusCreated || !reflect.DeepEqual(keysOf(got), []string{"id", "name"}) {
		t.Fatalf("create: %d %v", rec.Code, got)
	}
	path := itemsPath + "/" + got["id"].(string)
	if rec, got = f.do(t, "PUT", path+"?fields=age", `{"name": "Gadget", "age": 4}`); rec.Code != http.StatusOK || !reflect.DeepEqual(keysOf(got), []string{"age", "id"}) {
		t.Errorf("put: %d %v", rec.Code, got)
	}
	if rec, got = f.do(t, "PATCH", path+"?fields="+url.QueryEscape("_meta.version"), `{"age": 5}`); rec.Code != http.StatusOK || got["_meta"].(map[string]any)["version"] != float64(3) {
		t.Errorf("patch: %d %v", rec.Code, got)
	}
	// The stored document is whole.
	if _, stored := f.do(t, "GET", path, ""); stored["name"] != "Gadget" || stored["age"] != float64(5) {
		t.Errorf("stored = %v", stored)
	}
}

func TestBadFieldsAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := newFixture(t)
	for _, req := range []struct{ method, path, body string }{
		{"GET", itemsPath + "?fields=nope", ""},
		{"POST", itemsPath + "?fields=nope", `{"name": "Widget"}`},
	} {
		rec, got := f.do(t, req.method, req.path, req.body)
		if rec.Code != http.StatusBadRequest || errCode(got) != "invalid_query" {
			t.Errorf("%s %s: %d %v", req.method, req.path, rec.Code, got)
		}
		details, _ := got["error"].(map[string]any)["details"].([]any)
		if len(details) == 0 || details[0].(map[string]any)["path"] != "fields.nope" {
			t.Errorf("details = %v", details)
		}
	}
	if _, page := f.do(t, "GET", itemsPath, ""); len(page["items"].([]any)) != 0 {
		t.Errorf("a refused create wrote a document: %v", page)
	}
}

// expand is computed from the stored document: it works when the reference field is not selected,
// and the source field itself shows only if selected (design/field-projection.md §3).
func TestFieldsKeepExpandWorkingWithoutItsSource(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	cat := x.ids["go"]
	x.create(t, x.ada, "post", app+"posts", fmt.Sprintf(`{"title": "Hello", "category": %q}`, cat))
	path := app + "posts/" + x.ids["post"]

	rec, got := x.doH(t, "GET", path+"?expand=category(name)&fields=title", "", bearer(x.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %v", rec.Code, got)
	}
	if got["category"] != nil {
		t.Errorf("the source field was not selected, yet it is returned: %v", got["category"])
	}
	expanded, _ := got["_expanded"].(map[string]any)
	if c, _ := expanded["category"].(map[string]any); c["name"] != "Go" {
		t.Errorf("_expanded = %v", got["_expanded"])
	}
	_, got = x.doH(t, "GET", path+"?expand=category(name)&fields=title,category", "", bearer(x.ada))
	if got["category"] != cat || got["title"] != "Hello" {
		t.Errorf("with the source selected: %v", got)
	}
}
