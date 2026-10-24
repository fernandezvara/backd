package exportreadme

import (
	"strings"
	"testing"
	"time"
)

func readmeSample() ReadmeData {
	return ReadmeData{
		Realm: "Acme <Shop>", Email: "ada@example.com", Generated: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), Days: 7,
		Count:       map[string]int{"account": 1, "sessions": 3, "audit": 12},
		Collections: []ReadmePart{{Part: "main/orders.jsonl", Documents: 4}, {Part: "main/notes.jsonl", Documents: 2}},
		Files:       5,
		Exclusions:  []ReadmeExclusion{{What: "main/fraud_scores", Reason: "exempt under <the law>"}},
		Truncated:   []string{"audit"},
	}
}

func TestDefaultReadmesRenderEveryPart(t *testing.T) {
	for _, loc := range DefaultReadmeLocales() {
		src, ok := DefaultReadme(loc)
		if !ok {
			t.Fatalf("%s: no template", loc)
		}
		if err := CheckReadme(string(src)); err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		out, err := RenderReadme(string(src), readmeSample())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for _, want := range []string{"Acme &lt;Shop&gt;", "ada@example.com", "2026-10-01 09:30 UTC", "main/orders.jsonl", "main/notes.jsonl", "exempt under &lt;the law&gt;", ">12<", ">3<", "5"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: README lacks %q", loc, want)
			}
		}
		// Values are escaped, so nothing a user's data holds can open a tag.
		if strings.Contains(out, "<Shop>") || strings.Contains(out, "<the law>") {
			t.Errorf("%s: a value was not escaped", loc)
		}
		// Sections of what isn't there disappear.
		bare, _ := RenderReadme(string(src), ReadmeData{Realm: "Acme", Email: "a@b.c", Generated: time.Now(), Count: map[string]int{}})
		if strings.Contains(bare, "{{") || strings.Contains(bare, "<th>Why</th>") || strings.Contains(bare, "<th>Por qué</th>") {
			t.Errorf("%s: an empty export still shows exclusions or raw tags", loc)
		}
	}
	if len(DefaultReadmeLocales()) < 2 {
		t.Error("English and Spanish ship")
	}
}

func TestCheckReadmeSaysWhere(t *testing.T) {
	if err := CheckReadme("<p>{{Realm}}\n{{#Exclusions}}never closed"); err == nil || !strings.Contains(err.Error(), "line") {
		t.Errorf("unclosed section: %v", err)
	}
	if err := CheckReadme("<p>{{{raw}}}</p>"); err == nil {
		t.Error("raw output is not allowed")
	}
	if err := CheckReadme("<p>{{Realm}}</p>"); err != nil {
		t.Error(err)
	}
}
