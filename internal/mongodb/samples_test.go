package mongodb

import (
	"bytes"
	"encoding/json"
	"go.yaml.in/yaml/v3"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The repository's sample collections, which the differential tests run
// against: the example config, the template sample and the JavaScript
// client's integration configs. New samples are picked up automatically.
var sampleRoots = []string{"examples/config", "internal/templates/files", "clients/js/test/integration/config"}

// sample is one collection directory with a schema.json and maybe rules in its collection.yaml.
type sample struct {
	path   string // relative to the repository root
	schema string
	rules  string // "" without a rules: section
	config string // collection.yaml, "" without one (file fields are declared there)
}

func samples(t *testing.T) []sample {
	t.Helper()
	var out []sample
	for _, root := range sampleRoots {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "schema.json" {
				return err
			}
			dir := filepath.Dir(p)
			schema, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			config, err := os.ReadFile(filepath.Join(dir, "collection.yaml"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			// The rules are the `rules:` section of collection.yaml, as YAML again.
			var section struct {
				Rules map[string]string `yaml:"rules"`
			}
			if err := yaml.Unmarshal(config, &section); err != nil {
				return err
			}
			var rules []byte
			if len(section.Rules) > 0 {
				if rules, err = yaml.Marshal(section.Rules); err != nil {
					return err
				}
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), dir)
			out = append(out, sample{path: rel, schema: string(schema), rules: string(rules), config: string(config)})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) < 5 {
		t.Fatalf("found only %d sample collections under %v", len(out), sampleRoots)
	}
	return out
}

// docGen generates JSON documents for a schema: mostly valid, with some
// values of the wrong type, out of bounds, missing, duplicated or extra, so
// that validators have both kinds to judge. Values come from small pools,
// which makes documents collide with test users' ids and emails and with
// each other, as rules and unique checks need.
type docGen struct {
	rng       *rand.Rand
	wrongRate int // one value in wrongRate is deliberately off; 0: never
}

var (
	stringPool = []string{`""`, `"a"`, `"ES"`, `"abc"`, `"news"`, `"ñandú ☃"`, `"u1"`, `"u2"`, `"u3"`,
		`"ada@example.com"`, `"bob@example.com"`, `"cy@example.com"`, `"not an email"`, `"INV-000123"`, `"INV-12"`, `"active"`, `"EUR"`,
		`"559cca164c904a83a18c6358"`} // the last is 24 characters, for schemas with a minLength like a share token
	integerPool = []string{`0`, `1`, `3`, `-1`, `1.0`, `1e2`, `-0`, `7.0e0`, `9007199254740993`,
		`9223372036854775807`, `-9223372036854775808`, `9223372036854775808`, `1e30`}
	numberPool = append(slices.Clone(integerPool), `0.5`, `1.5`, `2.25`, `-1e308`, `1e-3`, `2.5`)
	allTypes   = []string{"string", "integer", "number", "boolean", "null", "array", "object"}
)

func (g docGen) pick(pool []string) string { return pool[g.rng.IntN(len(pool))] }
func (g docGen) off() bool                 { return g.wrongRate > 0 && g.rng.IntN(g.wrongRate) == 0 }

// value returns a JSON value for the schema.
func (g docGen) value(s map[string]any, depth int) string {
	if enum, ok := s["enum"].([]any); ok {
		if g.off() {
			return `"not-in-enum"`
		}
		b, _ := json.Marshal(enum[g.rng.IntN(len(enum))])
		return string(b)
	}
	var types []string
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, x := range t {
			types = append(types, x.(string))
		}
	default:
		types = allTypes
	}
	typ := types[g.rng.IntN(len(types))]
	if g.off() {
		typ = allTypes[g.rng.IntN(len(allTypes))]
	}
	if depth > 3 && (typ == "array" || typ == "object") {
		typ = "string"
	}
	switch typ {
	case "string":
		pool := slices.Clone(stringPool)
		if n, ok := s["maxLength"].(float64); ok {
			pool = append(pool, strconv.Quote(strings.Repeat("x", int(n))), strconv.Quote(strings.Repeat("x", int(n)+1)))
		}
		if g.off() {
			return g.pick(pool)
		}
		// Mostly strings that meet the constraints, if any in the pool do.
		if fit := slices.DeleteFunc(slices.Clone(pool), func(q string) bool { return !fits(s, q) }); len(fit) > 0 {
			return g.pick(fit)
		}
		return g.pick(pool)
	case "integer", "number":
		pool := integerPool
		if typ == "number" {
			pool = numberPool
		}
		pool = append(slices.Clone(pool), bounds(s)...)
		if g.off() {
			return g.pick(pool)
		}
		// Mostly numbers within the schema's bounds, if any in the pool are.
		if fit := slices.DeleteFunc(slices.Clone(pool), func(q string) bool { return !fitsNumber(s, typ, q) }); len(fit) > 0 {
			return g.pick(fit)
		}
		return g.pick(pool)
	case "boolean":
		return g.pick([]string{`true`, `false`})
	case "null":
		return `null`
	case "array":
		items, _ := s["items"].(map[string]any)
		// Mostly within minItems and without duplicates when uniqueItems
		// asks for it; off() breaks either now and then.
		n := g.rng.IntN(4)
		if min, ok := s["minItems"].(float64); ok && !g.off() {
			n = max(n, int(min))
		}
		unique := s["uniqueItems"] == true && !g.off()
		var vals []string
		for tries := 0; len(vals) < n && tries < 10*n; tries++ {
			v := g.value(items, depth+1)
			if unique && slices.Contains(vals, v) {
				continue
			}
			vals = append(vals, v)
		}
		if len(vals) > 0 && g.off() {
			vals = append(vals, vals[0]) // breaks uniqueItems
		}
		return "[" + strings.Join(vals, ",") + "]"
	default:
		return g.object(s, depth)
	}
}

// object returns an object for the schema: required properties are almost
// always there, optional ones about half the time.
func (g docGen) object(s map[string]any, depth int) string {
	props, _ := s["properties"].(map[string]any)
	required := map[string]bool{}
	for _, r := range asList(s["required"]) {
		required[r.(string)] = true
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	var parts []string
	for _, name := range names {
		keep := g.rng.IntN(2) == 0
		if required[name] {
			keep = !g.off()
		}
		if keep {
			sub, _ := props[name].(map[string]any)
			parts = append(parts, strconv.Quote(name)+":"+g.value(sub, depth+1))
		}
	}
	if g.off() {
		parts = append(parts, `"extra":1`) // breaks additionalProperties: false
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// fits reports whether the quoted string meets the schema's length,
// pattern and email format; other keywords are left to the validators.
func fits(s map[string]any, quoted string) bool {
	v, _ := strconv.Unquote(quoted)
	n := float64(utf8.RuneCountInString(v))
	if min, ok := s["minLength"].(float64); ok && n < min {
		return false
	}
	if max, ok := s["maxLength"].(float64); ok && n > max {
		return false
	}
	if p, ok := s["pattern"].(string); ok {
		if re, err := regexp.Compile(p); err == nil && !re.MatchString(v) {
			return false
		}
	}
	if s["format"] == "email" && !strings.Contains(v, "@") {
		return false
	}
	return true
}

// fitsNumber reports whether the JSON number meets the schema's bounds,
// and is whole for integers; multipleOf is left to the validators.
func fitsNumber(s map[string]any, typ, q string) bool {
	n, err := strconv.ParseFloat(q, 64)
	if err != nil || (typ == "integer" && n != math.Trunc(n)) {
		return false
	}
	lim := func(k string) (float64, bool) { v, ok := s[k].(float64); return v, ok }
	if v, ok := lim("minimum"); ok && n < v {
		return false
	}
	if v, ok := lim("maximum"); ok && n > v {
		return false
	}
	if v, ok := lim("exclusiveMinimum"); ok && n <= v {
		return false
	}
	if v, ok := lim("exclusiveMaximum"); ok && n >= v {
		return false
	}
	return true
}

// bounds returns numbers at and just past the schema's numeric limits.
func bounds(s map[string]any) []string {
	var out []string
	for _, k := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if n, ok := s[k].(float64); ok {
			out = append(out, fmtNum(n), fmtNum(n-1), fmtNum(n+1), fmtNum(n+0.5))
		}
	}
	return out
}

func fmtNum(n float64) string { return strconv.FormatFloat(n, 'g', -1, 64) }

func asList(v any) []any { l, _ := v.([]any); return l }

// decodeJSON decodes like the API does, keeping numbers exact.
func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("generated invalid JSON %s: %v", s, err)
	}
	return v
}

func parseSchema(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
