package auth

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseScopes(t *testing.T) {
	got, err := ParseScopes([]string{"read", "write:blog/posts", "call:main", "read:blog", "read"})
	if err != nil {
		t.Fatal(err)
	}
	want := Scopes{{Op: ScopeRead}, {Op: ScopeWrite, Database: "blog", Name: "posts"}, {Op: ScopeCall, Database: "main"}, {Op: ScopeRead, Database: "blog"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(got.Strings(), []string{"read", "write:blog/posts", "call:main", "read:blog"}) {
		t.Errorf("strings = %v", got.Strings())
	}
	for _, bad := range []string{"", "admin", "readwrite", "read:", "read:Blog", "read:blog/", "read:blog/posts/x", "read:/posts", "write:blog posts", "READ"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("ParseScope(%q) accepted", bad)
		}
	}
	many := make([]string, MaxScopes+1)
	for i := range many {
		many[i] = "read"
	}
	if _, err := ParseScopes(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
}

func TestScopesAllow(t *testing.T) {
	none := Scopes(nil)
	if !none.Allows(ScopeWrite, "any", "thing") || none.Limited() {
		t.Error("a key without scopes has full access")
	}
	s, _ := ParseScopes([]string{"read:blog", "write:blog/posts", "call:main/export", "read:shop/orders"})
	for _, tt := range []struct {
		op       ScopeOp
		db, name string
		want     bool
	}{
		{ScopeRead, "blog", "posts", true},
		{ScopeRead, "blog", "comments", true},   // the database
		{ScopeRead, "shop", "orders", true},     // the collection
		{ScopeRead, "shop", "customers", false}, // another collection
		{ScopeRead, "other", "posts", false},
		{ScopeWrite, "blog", "posts", true},
		{ScopeWrite, "blog", "comments", false}, // write was granted on posts only
		{ScopeWrite, "shop", "orders", false},   // read doesn't give write
		{ScopeCall, "main", "export", true},
		{ScopeCall, "main", "refund", false},
		{ScopeCall, "blog", "export", false},
	} {
		if got := s.Allows(tt.op, tt.db, tt.name); got != tt.want {
			t.Errorf("%s on %s/%s = %v, want %v", tt.op, tt.db, tt.name, got, tt.want)
		}
	}
	all, _ := ParseScopes([]string{"read", "call"})
	if !all.Allows(ScopeRead, "x", "y") || !all.Allows(ScopeCall, "x", "y") || all.Allows(ScopeWrite, "x", "y") {
		t.Error("bare grants cover everything of their kind only")
	}
}
