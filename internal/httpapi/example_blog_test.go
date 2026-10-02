package httpapi

import (
	"net/url"
	"strings"
	"testing"
)

// The blog example (examples/config/blog): its rules must allow normal
// use, stop the attacks listed in the docs page "Blog", and have the
// limits that page documents.

const blogPosts = "/v1/blog/main/posts"

// Erasing an author keeps their posts but removes the byline's email, replaces
// its name and clears the owner (the policy of examples/config/blog).
func TestBlogErasurePolicy(t *testing.T) {
	f := newExampleFixture(t, "blog")
	c, ok := f.reg.Collection("blog", "main", "posts")
	if !ok || c.Erasure == nil {
		t.Fatal("the posts have no erasure policy")
	}
	p := c.Erasure
	if p.Action != "anonymize" || len(p.Remove) != 1 || p.Remove[0] != "author.email" || p.Replace["author.name"] != "Erased author" {
		t.Errorf("policy: %+v", p)
	}
}

func TestBlogExample(t *testing.T) {
	f := newExampleFixture(t, "blog")
	ada, bob := f.signup(t, "ada@example.com"), f.signup(t, "bob@example.com")
	post := func(title string, published bool, email string) string {
		p := "false"
		if published {
			p = "true"
		}
		return `{"title": "` + title + `", "published": ` + p + `, "author": {"name": "Ada", "email": "` + email + `"}}`
	}

	pub := f.want(t, "ada publishes a post", 201, ada, "POST", blogPosts, post("Hello", true, "ada@example.com"))
	draft := f.want(t, "ada writes a draft", 201, ada, "POST", blogPosts, post("Draft", false, "ada@example.com"))
	pubPath, draftPath := blogPosts+"/"+pub["id"].(string), blogPosts+"/"+draft["id"].(string)
	titles := func(tok, where string) string {
		t.Helper()
		path := blogPosts
		if where != "" {
			path += "?where=" + url.QueryEscape(where)
		}
		_, out := f.as(t, tok, "GET", path, "")
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["title"].(string))
		}
		return strings.Join(got, ",")
	}

	t.Run("normal use", func(t *testing.T) {
		if got := titles("", ""); got != "Hello" {
			t.Errorf("anonymous list = %q, want the published post only", got)
		}
		if got := titles(ada, `{"published": false}`); got != "Draft" {
			t.Errorf("ada's drafts = %q", got)
		}
		f.want(t, "anonymous reads a published post", 200, "", "GET", pubPath, "")
		f.want(t, "ada edits her post", 200, ada, "PATCH", pubPath, `{"title": "Hello again", "category": "news"}`)
		f.want(t, "ada publishes her draft", 200, ada, "PATCH", draftPath, `{"published": true}`)
		f.want(t, "ada unpublishes it again", 200, ada, "PATCH", draftPath, `{"published": false}`)
		if got := titles("", `{"author.email": "ada@example.com", "category": "news"}`); got != "Hello again" {
			t.Errorf("filter by author and category = %q", got)
		}
	})

	t.Run("stopped by rules", func(t *testing.T) {
		f.want(t, "anonymous reads a draft", 404, "", "GET", draftPath, "")
		f.want(t, "another user reads a draft", 404, bob, "GET", draftPath, "")
		if got := titles(bob, `{"$or": [{"published": true}, {"published": false}]}`); strings.Contains(got, "Draft") {
			t.Errorf("an $or listed a draft to another user: %q", got)
		}
		f.want(t, "anonymous writes a post", 401, "", "POST", blogPosts, post("Spam", true, "ada@example.com"))
		f.want(t, "bob signs a post with ada's email", 403, bob, "POST", blogPosts, post("Fake", true, "ada@example.com"))
		f.want(t, "bob writes a post without an author", 403, bob, "POST", blogPosts, `{"title": "Anonymous", "published": true}`)
		f.want(t, "bob edits ada's post", 403, bob, "PATCH", pubPath, `{"title": "Hacked"}`)
		f.want(t, "bob edits ada's draft", 404, bob, "PATCH", draftPath, `{"title": "Hacked"}`)
		f.want(t, "bob deletes ada's post", 403, bob, "DELETE", pubPath, "")
		f.want(t, "ada re-attributes her post to bob", 403, ada, "PATCH", pubPath, `{"author": {"name": "Bob", "email": "bob@example.com"}}`)
		f.want(t, "ada changes only the author's name", 403, ada, "PATCH", pubPath, `{"author": {"name": "Someone else"}}`)
		f.want(t, "a post without a title", 400, ada, "POST", blogPosts, `{"published": true, "author": {"email": "ada@example.com"}}`)
		f.want(t, "an unknown field type", 400, ada, "POST", blogPosts, `{"title": "x", "published": "yes", "author": {"email": "ada@example.com"}}`)
	})

	// Limits documented on the docs page.
	t.Run("limits", func(t *testing.T) {
		f.want(t, "the author's display name is free text", 201, bob, "POST", blogPosts,
			`{"title": "By Ada?", "published": true, "author": {"name": "Ada Lovelace", "email": "bob@example.com"}}`)
		f.want(t, "unverified emails may post", 201, f.signup(t, "carl@example.com"), "POST", blogPosts,
			`{"title": "Unverified", "published": true, "author": {"email": "carl@example.com"}}`)
		f.want(t, "a post without published is a draft, visible to its author", 201, ada, "POST", blogPosts,
			`{"title": "No flag", "author": {"email": "ada@example.com"}}`)
		if got := titles("", ""); strings.Contains(got, "No flag") {
			t.Errorf("a post without published was listed publicly: %q", got)
		}
	})
}
