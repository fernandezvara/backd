package registry

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// findings loads a collection with the given versions (v1, v2, …) and returns its findings as
// "kind path rule" lines, with "(note)" when min_schema_version excuses them.
func findings(t *testing.T, minVersion int, versions ...string) []string {
	t.Helper()
	tree := map[string]string{}
	for i, v := range versions {
		tree[fmt.Sprintf("shop/main/items/schema.v%d.json", i+1)] = v
		if i > 0 {
			tree[fmt.Sprintf("shop/main/items/schema.v%d.ts", i+1)] = "export default (d: unknown) => d;"
		}
	}
	if minVersion > 0 {
		tree["shop/main/items/collection.yaml"] = fmt.Sprintf("min_schema_version: %d\n", minVersion)
	}
	reg, err := Load(writeTree(t, tree))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "items")
	fs, err := c.CompatibilityFindings()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range fs {
		line := fmt.Sprintf("v%d→v%d %s %s %s", f.From, f.To, f.Kind, f.Path, f.Rule)
		if f.Note {
			line += " (note)"
		}
		out = append(out, strings.ReplaceAll(line, "  ", " "))
	}
	return out
}

func obj(props, rest string) string {
	if rest != "" {
		rest = "," + rest
	}
	return `{"type":"object","properties":{` + props + `}` + rest + `}`
}

func TestCompatibilityFindings(t *testing.T) {
	const title = `"title":{"type":"string"}`
	for name, tc := range map[string]struct {
		lower, upper string
		min          int
		want         []string
	}{
		"an optional field added is silent": {obj(title, ""), obj(title+`,"slug":{"type":"string"}`, ""), 0, nil},
		"nothing changed is silent":         {obj(title, ""), obj(title, ""), 0, nil},
		"a required field added":            {obj(title, ""), obj(title+`,"slug":{"type":"string"}`, `"required":["slug"]`), 0, []string{"v1→v2 refuses slug required"}},
		"a required field that was already": {obj(title, `"required":["title"]`), obj(title+`,"slug":{"type":"string"}`, `"required":["title"]`), 0, nil},
		"an enum narrowed":                  {obj(`"k":{"enum":["a","b","c"]}`, ""), obj(`"k":{"enum":["a","b"]}`, ""), 0, []string{"v1→v2 refuses k enum"}},
		"an enum widened is silent":         {obj(`"k":{"enum":["a","b"]}`, ""), obj(`"k":{"enum":["a","b","c"]}`, ""), 0, nil},
		"an enum added":                     {obj(`"k":{"type":"string"}`, ""), obj(`"k":{"type":"string","enum":["a"]}`, ""), 0, []string{"v1→v2 refuses k enum"}},
		"maxLength smaller":                 {obj(`"t":{"type":"string","maxLength":100}`, ""), obj(`"t":{"type":"string","maxLength":50}`, ""), 0, []string{"v1→v2 refuses t maxLength"}},
		"maxLength larger is silent":        {obj(`"t":{"type":"string","maxLength":50}`, ""), obj(`"t":{"type":"string","maxLength":100}`, ""), 0, nil},
		"minLength larger":                  {obj(`"t":{"type":"string","minLength":1}`, ""), obj(`"t":{"type":"string","minLength":5}`, ""), 0, []string{"v1→v2 refuses t minLength"}},
		"a minimum appears":                 {obj(`"n":{"type":"integer"}`, ""), obj(`"n":{"type":"integer","minimum":0}`, ""), 0, []string{"v1→v2 refuses n minimum"}},
		"a pattern appears":                 {obj(`"t":{"type":"string"}`, ""), obj(`"t":{"type":"string","pattern":"^a"}`, ""), 0, []string{"v1→v2 refuses t pattern"}},
		"a pattern changes":                 {obj(`"t":{"type":"string","pattern":"^a"}`, ""), obj(`"t":{"type":"string","pattern":"^b"}`, ""), 0, []string{"v1→v2 unsure t pattern"}},
		"additionalProperties false set":    {obj(title, ""), obj(title, `"additionalProperties":false`), 0, []string{"v1→v2 refuses additionalProperties"}},
		"additionalProperties false before": {obj(title, `"additionalProperties":false`), obj(title+`,"x":{"type":"string"}`, `"additionalProperties":false`), 0, nil},
		"a field dropped":                   {obj(title+`,"body":{"type":"string"}`, ""), obj(title, ""), 0, []string{"v1→v2 drops body properties"}},
		"a field dropped, min excuses it":   {obj(title+`,"body":{"type":"string"}`, ""), obj(title, ""), 1, []string{"v1→v2 drops body properties (note)"}},
		"a type changed":                    {obj(`"tags":{"type":"string"}`, ""), obj(`"tags":{"type":"array","items":{"type":"string"}}`, ""), 0, []string{"v1→v2 type tags type"}},
		"a type widened to include null":    {obj(`"d":{"type":"string"}`, ""), obj(`"d":{"type":["string","null"]}`, ""), 0, []string{"v1→v2 type d type"}},
		"a nested required field":           {obj(`"a":{"type":"object","properties":{"b":{"type":"string"}}}`, ""), obj(`"a":{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}`, ""), 0, []string{"v1→v2 refuses a.b required"}},
		"array items narrowed":              {obj(`"l":{"type":"array","items":{"type":"string","maxLength":40}}`, ""), obj(`"l":{"type":"array","items":{"type":"string","maxLength":10}}`, ""), 0, []string{"v1→v2 refuses l[] maxLength"}},
		"maxItems smaller":                  {obj(`"l":{"type":"array","maxItems":12}`, ""), obj(`"l":{"type":"array","maxItems":5}`, ""), 0, []string{"v1→v2 refuses l maxItems"}},
		"oneOf changed":                     {obj(`"v":{"oneOf":[{"type":"string"},{"type":"integer"}]}`, ""), obj(`"v":{"oneOf":[{"type":"string"}]}`, ""), 0, []string{"v1→v2 unsure v oneOf"}},
		"if/then added":                     {obj(title, ""), obj(title, `"if":{"properties":{"a":{"const":1}}},"then":{"required":["b"]}`), 0, []string{"v1→v2 unsure if", "v1→v2 unsure then"}},
		"a $ref target changed": {
			`{"type":"object","properties":{"a":{"$ref":"#/$defs/n"}},"$defs":{"n":{"type":"object","properties":{"t":{"type":"string"}}}}}`,
			`{"type":"object","properties":{"a":{"$ref":"#/$defs/n"}},"$defs":{"n":{"type":"object","properties":{"t":{"type":"string"}},"required":["t"]}}}`,
			0, []string{"v1→v2 refuses a.t required"},
		},
		"a recursive $ref terminates": {
			`{"type":"object","properties":{"n":{"$ref":"#/$defs/node"}},"$defs":{"node":{"type":"object","properties":{"c":{"$ref":"#/$defs/node"},"x":{"type":"string"}}}}}`,
			`{"type":"object","properties":{"n":{"$ref":"#/$defs/node"}},"$defs":{"node":{"type":"object","properties":{"c":{"$ref":"#/$defs/node"},"x":{"type":"string","maxLength":3}}}}}`,
			0, []string{"v1→v2 refuses n.x maxLength"},
		},
		"min_schema_version turns a refusal into a note": {obj(title, ""), obj(title+`,"slug":{"type":"string"}`, `"required":["slug"]`), 2, []string{"v1→v2 refuses slug required (note)"}},
		"a refusal is not excused by a lower minimum":    {obj(title, ""), obj(title+`,"slug":{"type":"string"}`, `"required":["slug"]`), 1, []string{"v1→v2 refuses slug required"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := findings(t, tc.min, tc.lower, tc.upper)
			if !slices.Equal(got, tc.want) {
				t.Errorf("findings = %q, want %q", got, tc.want)
			}
		})
	}
}

// The three versions of the design's §13: body becomes description, every asset gets a
// visibility. v2 only adds optional fields (silent); v3 stops declaring body, which min_schema_version: 2
// acknowledges; making visibility required in a v4 warns until min_schema_version: 3.
func TestCompatibilityOfTheDesignExample(t *testing.T) {
	v1 := `{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200},"kind":{"enum":["link","note","file"]},"url":{"type":"string","format":"uri"},"body":{"type":"string"},"tags":{"type":"array","items":{"type":"string","minLength":1,"maxLength":40},"maxItems":12},"published_at":{"type":["string","null"],"format":"date-time"},"downloads":{"type":"integer","minimum":0}},"required":["title","kind"]}`
	v2 := strings.Replace(v1, `"body":{"type":"string"},`, `"body":{"type":"string"},"description":{"type":"string"},"visibility":{"enum":["team","public"]},`, 1)
	v3 := strings.Replace(v2, `"body":{"type":"string"},`, ``, 1)
	v4 := strings.Replace(v3, `"required":["title","kind"]`, `"required":["title","kind","visibility"]`, 1)

	if got := findings(t, 0, v1, v2); len(got) != 0 {
		t.Errorf("v1→v2 (expand) should be silent: %q", got)
	}
	if got := findings(t, 0, v1, v2, v3); !slices.Equal(got, []string{"v2→v3 drops body properties"}) {
		t.Errorf("v2→v3 without min_schema_version: %q", got)
	}
	if got := findings(t, 2, v1, v2, v3); !slices.Equal(got, []string{"v2→v3 drops body properties (note)"}) {
		t.Errorf("v2→v3 with min_schema_version: 2 should be a note: %q", got)
	}
	if got := findings(t, 2, v1, v2, v3, v4); !slices.Equal(got, []string{"v2→v3 drops body properties (note)", "v3→v4 refuses visibility required"}) {
		t.Errorf("v4 requiring visibility, min 2: %q", got)
	}
	if got := findings(t, 4, v1, v2, v3, v4); !slices.Equal(got, []string{"v2→v3 drops body properties (note)", "v3→v4 refuses visibility required (note)"}) {
		t.Errorf("min 4 excuses everything: %q", got)
	}
}

func TestCompatibilityFindingsOfARealm(t *testing.T) {
	tree := map[string]string{
		"shop/main/items/schema.v1.json": obj(`"a":{"type":"string"}`, ""),
		"shop/main/items/schema.v2.json": obj(``, ""),
		"shop/main/items/schema.v2.ts":   "export default (d: unknown) => d;",
		"shop/main/plain/schema.json":    obj(`"a":{"type":"string"}`, ""),
	}
	reg, err := Load(writeTree(t, tree))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := reg.CompatibilityFindings()
	if err != nil || len(fs) != 1 {
		t.Fatalf("findings = %v, %v", fs, err)
	}
	if got := fs[0].String(); got != `warning: main/items schema.v1.json → schema.v2.json: a: "a" is no longer declared: apps on the lower version read documents without it` {
		t.Errorf("String() = %q", got)
	}
}
