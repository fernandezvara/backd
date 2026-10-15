package query

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseExpand(t *testing.T) {
	got, err := ParseExpand("author(name,avatar), tags(name),category,author.company(address.city)")
	if err != nil {
		t.Fatal(err)
	}
	want := []ExpandPath{
		{Names: []string{"author"}, Fields: []string{"name", "avatar"}, Text: "author(name,avatar)"},
		{Names: []string{"tags"}, Fields: []string{"name"}, Text: "tags(name)"},
		{Names: []string{"category"}, Text: "category"},
		{Names: []string{"author", "company"}, Fields: []string{"address.city"}, Text: "author.company(address.city)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got, err := ParseExpand(""); got != nil || err != nil {
		t.Errorf("empty = %v, %v", got, err)
	}
}

func TestParseExpandErrors(t *testing.T) {
	for in, want := range map[string]string{
		"author(name":     "unbalanced",
		"author)":         "unbalanced",
		"a,,b":            "an empty entry",
		"author(name))":   "unbalanced",
		"author(na me)":   `"na me" is not a field name`,
		"author()":        `"" is not a field name`,
		"author(a(b))":    "one pair of parentheses",
		"1author":         `"1author" is not a reference name`,
		"author..company": `"" is not a reference name`,
		"author(name)x":   "one pair of parentheses",
	} {
		_, err := ParseExpand(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}
	}
}
