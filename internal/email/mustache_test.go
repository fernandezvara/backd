package email

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string, data map[string]any, escape func(string) string) string {
	t.Helper()
	m, err := ParseMustache(src)
	if err != nil {
		t.Fatalf("%q: %v", src, err)
	}
	out, rerr := m.Render(data, escape)
	if rerr != nil {
		t.Fatalf("%q: %v", src, rerr)
	}
	return out
}

func TestMustacheRendering(t *testing.T) {
	data := map[string]any{
		"Link": "https://x.example/t?a=1&b=2",
		"User": map[string]any{"Email": "<ana@example.com>"},
		"App":  map[string]any{"name": "Acme", "logo": "", "colors": []any{"red", "blue"}},
		"Data": map[string]any{"days": 3, "ok": true},
	}
	for name, tc := range map[string]struct{ src, want string }{
		"variables":           {`Hi {{User.Email}}: {{ Link }}`, `Hi &lt;ana@example.com&gt;: https://x.example/t?a=1&amp;b=2`},
		"missing prints none": {`[{{App.nothing}}][{{Nope.x}}]`, `[][]`},
		"a number":            {`{{Data.days}} days`, `3 days`},
		"section":             {`{{#App.name}}Hello {{App.name}}{{/App.name}}`, `Hello Acme`},
		"section of an empty": {`{{#App.logo}}logo{{/App.logo}}`, ``},
		"inverted":            {`{{^App.logo}}text only{{/App.logo}}{{^App.name}}no name{{/App.name}}`, `text only`},
		"a list":              {`{{#App.colors}}<{{.}}>{{/App.colors}}`, `<red><blue>`},
		"a comment":           {`a{{! ignored }}b`, `ab`},
		"a structure":         {`[{{App}}]`, `[]`},
		"true":                {`{{#Data.ok}}yes{{/Data.ok}}`, `yes`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := render(t, tc.src, data, EscapeHTML); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Text isn't HTML: values are not entity-escaped there, but control characters go.
	if got := render(t, `{{User.Email}} {{Data.x}}`, map[string]any{"User": map[string]any{"Email": "a&b\x00\x07<c>"}}, EscapeText); got != "a&b<c> " {
		t.Errorf("text: %q", got)
	}
}

func TestMustacheRefusesWhatCanDoMore(t *testing.T) {
	for name, tc := range map[string]struct {
		src          string
		line, column int
		want         string
	}{
		"a partial":        {"hello\n  {{> header}}", 2, 3, "partials"},
		"raw output":       {"{{{Link}}}", 1, 1, "raw output"},
		"an ampersand":     {"x {{& Link}}", 1, 3, "raw output"},
		"delimiters":       {"{{=<% %>=}}", 1, 1, "delimiters"},
		"a helper":         {"{{upper Link}}", 1, 1, "helpers and expressions"},
		"an expression":    {"{{a || b}}", 1, 1, "helpers and expressions"},
		"unclosed tag":     {"a {{Link", 1, 3, "never closed"},
		"unclosed section": {"{{#a}}\n text", 1, 1, "never closed"},
		"stray close":      {"a\n{{/a}}", 2, 1, "isn't open"},
		"mismatch":         {"{{#a}}{{/b}}", 1, 7, "isn't open"},
		"too deep":         {strings.Repeat("{{#a}}", 7), 1, 37, "nest at most"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseMustache(tc.src)
			if err == nil || !strings.Contains(err.Message, tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if err.Line != tc.line || err.Column != tc.column {
				t.Errorf("at line %d column %d, want %d:%d (%s)", err.Line, err.Column, tc.line, tc.column, err.Message)
			}
		})
	}
}

func TestMustacheIsBounded(t *testing.T) {
	m, _ := ParseMustache(`{{#a}}{{#a}}x{{/a}}{{/a}}`)
	items := make([]any, 40)
	for i := range items {
		items[i] = "v"
	}
	// 40 outer × 40 inner repeats is more than a template may make.
	if _, err := m.Render(map[string]any{"a": items}, EscapeText); err == nil {
		t.Error("1600 repeats of a section were rendered")
	}
	if _, err := m.Render(map[string]any{"a": items[:10]}, EscapeText); err != nil {
		t.Errorf("110 repeats: %v", err)
	}
}

func TestSanitizeHTML(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"formatting stays":    {`<p>Hi <strong>you</strong><br><em>now</em></p>`, `<p>Hi <strong>you</strong><br><em>now</em></p>`},
		"a script goes":       {`<p>a</p><script>alert(1)</script><p>b</p>`, `<p>a</p><p>b</p>`},
		"event handlers go":   {`<p onclick="x()" id="a">a</p>`, `<p>a</p>`},
		"javascript links go": {`<a href="javascript:alert(1)">x</a>`, `<a rel="noopener noreferrer">x</a>`},
		"https links stay":    {`<a href="https://x.example/p?a=1&amp;b=2">x</a>`, `<a href="https://x.example/p?a=1&amp;b=2" rel="noopener noreferrer">x</a>`},
		"mailto stays":        {`<a href="mailto:a@example.com">x</a>`, `<a href="mailto:a@example.com" rel="noopener noreferrer">x</a>`},
		"http links go":       {`<a href="http://x.example/">x</a>`, `<a rel="noopener noreferrer">x</a>`},
		"https images stay":   {`<img src="https://x.example/l.png" alt="logo" width="120">`, `<img src="https://x.example/l.png" alt="logo" width="120">`},
		"data images go":      {`<img src="data:image/png;base64,AAAA">`, `<img>`},
		"a form goes":         {`<form action="https://evil.example"><input name="p"></form>after`, `after`},
		"an iframe goes":      {`<iframe src="https://evil.example"></iframe>ok`, `ok`},
		"unknown tags go":     {`<blink>x</blink><marquee>y</marquee>`, `xy`},
		"tables stay":         {`<table width="100%"><tr><td colspan="2">x</td></tr></table>`, `<table width="100%"><tr><td colspan="2">x</td></tr></table>`},
		"safe styles stay":    {`<p style="color: #336699; font-size: 14px">x</p>`, `<p style="color: #336699; font-size: 14px">x</p>`},
		"unsafe styles go":    {`<p style="background: url(https://evil.example/x); position: fixed; color: red">x</p>`, `<p style="color: red">x</p>`},
		"comments go":         {`a<!-- hidden -->b`, `ab`},
		"text is escaped":     {`1 &lt; 2 & 3`, `1 &lt; 2 &amp; 3`},
		"unbalanced closes":   {`<p><b>x`, `<p><b>x</b></p>`},
		"stray closes go":     {`x</div></p>`, `x`},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := SanitizeHTML(tc.in)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
	if _, removed := SanitizeHTML(`<script>x</script><a href="javascript:1">y</a>`); len(removed) != 2 {
		t.Errorf("removed = %v", removed)
	}
}

func TestCompileOverrideChecksWhatAKindNeeds(t *testing.T) {
	good := Override{Subject: "Reset {{App.name}}", Text: "Open {{Link}}", HTML: `<a href="{{Link}}">Reset</a>`}
	if _, problems := CompileOverride(ResetPassword, good); len(problems) > 0 {
		t.Fatalf("good: %v", problems)
	}
	_, problems := CompileOverride(ResetPassword, Override{Subject: "x", Text: "no link", HTML: "<p>no link</p>"})
	if len(problems) != 2 || !strings.Contains(problems[0].Message, "{{Link}}") {
		t.Errorf("link: %v", problems)
	}
	_, problems = CompileOverride(OrgInvitation, Override{Subject: "Join", Text: "{{Link}}", HTML: "{{Link}}"})
	if len(problems) != 2 || !strings.Contains(problems[0].Message, "Data.org_name") {
		t.Errorf("invitation: %v", problems)
	}
	if _, problems := CompileOverride("order-shipped", Override{Subject: "x", Text: "y", HTML: "z"}); len(problems) > 0 {
		t.Errorf("a custom kind needs nothing: %v", problems)
	}
	_, problems = CompileOverride(ResetPassword, Override{Subject: "", Text: "{{#a}}", HTML: "x" + strings.Repeat("a", MaxTemplateBytes)})
	if len(problems) < 3 {
		t.Errorf("problems: %v", problems)
	}
	c, _ := CompileOverride(ResetPassword, good)
	msg, removed, err := c.Render(Data{Realm: "blog", Link: "https://x.example/t", App: map[string]any{"name": "Acme <&>"}}, "Acme Corp")
	if err != nil || msg.Subject != "Reset Acme <&>" || msg.HTML != `<a href="https://x.example/t" rel="noopener noreferrer">Reset</a>` || len(removed) != 0 {
		t.Errorf("render: %+v %v %v", msg, removed, err)
	}
}
