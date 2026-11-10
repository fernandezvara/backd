package registry

import (
	"strings"
	"testing"
)

// parentRefs: posts.category points at categories (name, slug, tags), posts.tags is a list of ids.
const parentRefs = "references:\n  category:\n    collection: categories\n  tags:\n    collection: categories\n    many: true\n"

func TestParentRulesLoad(t *testing.T) {
	_, err := refTree(t, parentRefs+`rules:
  read: user != nil && user.id in parent(document.category).name
`, func(f map[string]string) {
		f["shop/main/categories/schema.json"] = `{"type":"object","properties":{"name":{"type":"string"},"slug":{"type":"string"},"members":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`
	})
	if err == nil || !strings.Contains(err.Error(), "parent.name") {
		// name is a string, `in` needs an array: the error is about the parent's schema
		t.Fatalf("err = %v", err)
	}
	reg, err := refTree(t, parentRefs+`rules:
  read: user != nil && user.id in parent(document.category).members
  create: user != nil && user.id in parent(data.category).members
`, func(f map[string]string) {
		f["shop/main/categories/schema.json"] = `{"type":"object","properties":{"name":{"type":"string"},"slug":{"type":"string"},"members":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "posts")
	uses := c.Rules.ParentUses()
	if len(uses) != 1 || uses[0].Ref != "category" || uses[0].Field != "category" {
		t.Fatalf("uses = %+v", uses)
	}
}

func TestParentRulesErrors(t *testing.T) {
	for name, tc := range map[string]struct{ rules, want string }{
		"not a reference":      {"read: user != nil && parent(document.title).name == user.id", `"title" isn't the source field of a reference`},
		"many":                 {"read: user != nil && parent(document.tags).name == user.id", "many: true"},
		"unknown parent field": {"read: parent(document.category).nope == 'x'", "unknown field parent.nope"},
		"no references":        {"read: parent(document.category).name == 'x'", ""},
	} {
		t.Run(name, func(t *testing.T) {
			refs := parentRefs
			if tc.want == "" {
				refs = ""
			}
			_, err := refTree(t, refs+"rules:\n  "+tc.rules+"\n", nil)
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "posts/collection.yaml") {
				t.Errorf("the error doesn't name the file: %v", err)
			}
		})
	}
}

func TestParentReadRuleRefusesRealtimeForNow(t *testing.T) {
	_, err := refTree(t, parentRefs+"realtime: true\nrules:\n  read: user != nil && parent(document.category).slug == user.id\n", nil)
	if err == nil || !strings.Contains(err.Error(), "parent()") {
		t.Fatalf("err = %v", err)
	}
}
