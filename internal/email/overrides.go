package email

import (
	"fmt"
	"strings"
	"time"
)

// Override is a company's own template of one kind in one language (design/team-access.md §11):
// the three parts of a message, in the logic-less syntax of ParseMustache.
type Override struct {
	Subject string
	Text    string
	HTML    string
}

// OverrideProblem is one thing wrong with an override, for the person who wrote it.
type OverrideProblem struct {
	Part         string // subject, text or html
	Line, Column int    // 0 when the problem isn't at a place
	Message      string
}

func (p OverrideProblem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s, line %d, column %d: %s", p.Part, p.Line, p.Column, p.Message)
	}
	return p.Part + ": " + p.Message
}

// CompiledOverride is a checked override, ready to render.
type CompiledOverride struct {
	subject, text, html *Mustache
}

// RequiredPlaceholders are the values a template of kind must print, so the message still does
// its job: the link of a token kind, and for an invitation the organization it is to.
func RequiredPlaceholders(kind string) []string {
	var out []string
	if TokenPurpose(kind) != "" || kind == OrgInvitation || kind == MagicLink {
		out = append(out, "Link")
	}
	if kind == OrgInvitation {
		out = append(out, "Data.org_name")
	}
	return out
}

// CompileOverride parses an override and checks it: sizes, syntax (with where), and the
// placeholders its kind needs in the text and the HTML.
func CompileOverride(kind string, o Override) (*CompiledOverride, []OverrideProblem) {
	var problems []OverrideProblem
	c := &CompiledOverride{}
	for _, part := range []struct {
		name string
		src  string
		to   **Mustache
	}{{"subject", o.Subject, &c.subject}, {"text", o.Text, &c.text}, {"html", o.HTML, &c.html}} {
		switch {
		case strings.TrimSpace(part.src) == "":
			problems = append(problems, OverrideProblem{Part: part.name, Message: "is required"})
			continue
		case len(part.src) > MaxTemplateBytes:
			problems = append(problems, OverrideProblem{Part: part.name, Message: fmt.Sprintf("is larger than %d KiB", MaxTemplateBytes>>10)})
			continue
		}
		m, err := ParseMustache(part.src)
		if err != nil {
			problems = append(problems, OverrideProblem{Part: part.name, Line: err.Line, Column: err.Column, Message: err.Message})
			continue
		}
		*part.to = m
	}
	for _, name := range RequiredPlaceholders(kind) {
		for _, part := range []struct {
			name string
			m    *Mustache
		}{{"text", c.text}, {"html", c.html}} {
			if part.m != nil && !part.m.Prints(name) {
				problems = append(problems, OverrideProblem{Part: part.name, Message: "must include {{" + name + "}}, which this kind of message needs"})
			}
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return c, nil
}

// Context is the data of an owner template, as maps: Realm, Link, ExpiresAt, User.Email,
// User.Locale, Data.*, App.* and Org.Name.
func (d Data) Context(orgName string) map[string]any {
	ctx := map[string]any{
		"Realm": d.Realm,
		"Link":  d.Link,
		"User":  map[string]any{"Email": d.User.Email, "Locale": d.User.Locale},
		"Data":  d.Data,
		"App":   d.App,
	}
	if !d.ExpiresAt.IsZero() {
		ctx["ExpiresAt"] = d.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	if orgName != "" {
		ctx["Org"] = map[string]any{"Name": orgName}
	}
	return ctx
}

// Render renders the override. Every value is escaped (HTML for the HTML part), and the HTML that
// results passes the allowlist sanitizer; removed says what it dropped.
func (c *CompiledOverride) Render(d Data, orgName string) (msg Message, removed []string, err error) {
	ctx := d.Context(orgName)
	subject, err := c.subject.Render(ctx, EscapeText)
	if err != nil {
		return Message{}, nil, fmt.Errorf("subject: %w", err)
	}
	text, err := c.text.Render(ctx, EscapeText)
	if err != nil {
		return Message{}, nil, fmt.Errorf("text: %w", err)
	}
	body, err := c.html.Render(ctx, EscapeHTML)
	if err != nil {
		return Message{}, nil, fmt.Errorf("html: %w", err)
	}
	clean, removed := SanitizeHTML(body)
	return Message{Subject: strings.Join(strings.Fields(subject), " "), Text: text, HTML: clean}, removed, nil
}

// SampleData is what a template of kind is checked and previewed with.
func SampleData(kind string) Data {
	d := sample
	d.Data = map[string]any{}
	for k, v := range sample.Data {
		d.Data[k] = v
	}
	d.ExpiresAt = time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	if TokenPurpose(kind) == "" && kind != OrgInvitation && kind != MagicLink {
		d.Link = ""
	}
	return d
}
