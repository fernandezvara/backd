package registry

import (
	"strings"
	"testing"
)

const articlesSchema = `{
  "type": "object",
  "properties": {
    "title":  {"type": "string"},
    "body":   {"type": ["string", "null"]},
    "tags":   {"type": "array", "items": {"type": "string"}},
    "views":  {"type": "integer"},
    "lang":   {"type": "string"},
    "author": {"type": "object", "properties": {"name": {"type": "string"}}},
    "lines":  {"type": "array", "items": {"type": "object", "properties": {"note": {"type": "string"}}}}
  }
}`

func articlesTree(t *testing.T, indexes string) (*Registry, error) {
	t.Helper()
	return Load(writeTree(t, map[string]string{
		"shop/realm.yaml":                 "signup: open\n",
		"shop/main/articles/schema.json":  articlesSchema,
		"shop/main/articles/indexes.json": indexes,
	}))
}

func TestTextIndexDeclared(t *testing.T) {
	reg, err := articlesTree(t, `[{"fields": ["lang"]}, {"text": {"fields": {"title": 10, "tags": 5, "body": 1, "author.name": 2}, "default_language": "es", "language_field": "lang"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "articles")
	x := c.Text
	if x == nil || x.DefaultLanguage != "spanish" || x.LanguageField != "lang" || len(x.Fields) != 4 || x.Fields[0].Path != "author.name" || x.Fields[3].Weight != 10 {
		t.Fatalf("Text = %+v", x)
	}
	if len(c.Indexes) == 0 {
		t.Error("the plain indexes are kept")
	}
	// Defaults.
	reg, err = articlesTree(t, `[{"text": {"fields": {"title": 1}}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := reg.Collection("shop", "main", "articles"); c.Text.DefaultLanguage != "english" || c.Text.LanguageField != "" {
		t.Errorf("defaults = %+v", c.Text)
	}
}

func TestTextIndexErrors(t *testing.T) {
	for name, tc := range map[string]struct{ indexes, want string }{
		"no fields":        {`[{"text": {}}]`, "text.fields must name at least one field"},
		"unknown":          {`[{"text": {"fields": {"nope": 1}}}]`, "text.fields.nope: field is not declared"},
		"a number":         {`[{"text": {"fields": {"views": 1}}}]`, "only strings and arrays of strings"},
		"an object":        {`[{"text": {"fields": {"author": 1}}}]`, "only strings and arrays of strings"},
		"in an array":      {`[{"text": {"fields": {"lines.note": 1}}}]`, "inside an array of objects"},
		"weight":           {`[{"text": {"fields": {"title": 0}}}]`, "must be between 1 and 1000"},
		"two":              {`[{"text": {"fields": {"title": 1}}}, {"text": {"fields": {"body": 1}}}]`, "only one text index"},
		"language":         {`[{"text": {"fields": {"title": 1}, "default_language": "klingon"}}]`, `"klingon" is not a language the database supports`},
		"language field":   {`[{"text": {"fields": {"title": 1}, "language_field": "nope"}}]`, `text.language_field: "nope" is not declared`},
		"not a string":     {`[{"text": {"fields": {"title": 1}, "language_field": "views"}}]`, "must be a string field"},
		"with fields":      {`[{"fields": ["title"], "text": {"fields": {"title": 1}}}]`, `a "text" entry has no "fields" list`},
		"unknown property": {`[{"text": {"fields": {"title": 1}, "stemmer": "x"}}]`, "unknown field"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := articlesTree(t, tc.indexes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSearchLanguage(t *testing.T) {
	for in, want := range map[string]string{"es": "spanish", "es-MX": "spanish", "PT_br": "portuguese", "en-GB": "english", "nb": "norwegian", "zh": "none", "": "none", "xx": "none"} {
		if got := SearchLanguage(in); got != want {
			t.Errorf("SearchLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
