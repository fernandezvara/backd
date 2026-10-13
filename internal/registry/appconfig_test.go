package registry

import (
	"strings"
	"testing"
)

const appConfigRealm = "roles:\n  beta: {}\nconsents:\n  marketing:\n    current: 1\n"

const appConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "maintenance": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "enabled": {"type": "boolean", "default": false, "description": "Show the banner"},
        "message": {"type": "string", "maxLength": 500, "default": ""}
      }
    },
    "features": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "reports": {
          "type": "object", "additionalProperties": false,
          "properties": {
            "enabled":   {"type": "boolean", "default": true},
            "max_range": {"type": "string", "pattern": "^[0-9]+d$", "default": "365d"}
          }
        }
      }
    },
    "beta": {
      "type": "object", "additionalProperties": false,
      "properties": {"new_editor": {"type": "boolean", "default": false}, "note": {"type": ["string", "null"]}}
    }
  }
}`

const appConfigRules = `read:
  maintenance: "true"
  features: user != nil
  beta: hasRole(user, "beta")
  features.reports: hasConsent(user, "marketing")
`

func appConfigTree(schema, rules string) map[string]string {
	files := map[string]string{
		"shop/realm.yaml":                 appConfigRealm,
		"shop/consents/marketing/1/en.md": "Marketing",
		"shop/app-config/schema.json":     schema,
	}
	if rules != "" {
		files["shop/app-config/rules.yaml"] = rules
	}
	return files
}

func TestAppConfigLoad(t *testing.T) {
	reg, err := Load(writeTree(t, appConfigTree(appConfigSchema, appConfigRules)))
	if err != nil {
		t.Fatal(err)
	}
	a := reg.Realms["shop"].AppConfig
	if a == nil {
		t.Fatal("no app configuration")
	}
	for _, p := range []string{"maintenance", "maintenance.enabled", "features.reports.max_range", "beta.note"} {
		if !a.HasPath(p) {
			t.Errorf("path %s missing; have %v", p, a.SortedPaths())
		}
	}
	if !a.Paths["maintenance.enabled"].Leaf || a.Paths["maintenance"].Leaf {
		t.Error("leaf and branch mixed up")
	}
	feat := a.Defaults["features"].(map[string]any)["reports"].(map[string]any)
	if feat["enabled"] != true || feat["max_range"] != "365d" {
		t.Errorf("defaults = %v", a.Defaults)
	}
	if v, ok := a.Defaults["beta"].(map[string]any)["note"]; !ok || v != nil {
		t.Errorf("a nullable leaf defaults to null: %v", a.Defaults["beta"])
	}
	if a.Paths["maintenance.enabled"].Description != "Show the banner" {
		t.Errorf("description = %q", a.Paths["maintenance.enabled"].Description)
	}

	// Own rule ANDed with the ancestors'.
	rs, ok := a.Effective("features.reports.enabled")
	if !ok || len(rs) != 2 {
		t.Errorf("features.reports.enabled has %d rules, ok=%v; want features and features.reports", len(rs), ok)
	}
	if rs, ok := a.Effective("maintenance.message"); !ok || len(rs) != 1 || !rs[0].AlwaysTrue() {
		t.Errorf("maintenance.message rules = %v, %v", rs, ok)
	}
	if rs, _ := a.Effective("features"); len(rs) != 1 || rs[0].AlwaysTrue() {
		t.Errorf("features rules = %v", rs)
	}
}

func TestAppConfigWithoutRulesIsUnreadable(t *testing.T) {
	reg, err := Load(writeTree(t, appConfigTree(appConfigSchema, "")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Realms["shop"].AppConfig.Effective("maintenance.enabled"); ok {
		t.Error("a path with no rule is readable")
	}
}

func TestAppConfigRefusals(t *testing.T) {
	leaf := func(body string) string {
		return `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"object","additionalProperties":false,"properties":{"b":` + body + `}}}}`
	}
	tests := []struct {
		name, schema, rules, want string
	}{
		{"open object", `{"type":"object","properties":{"a":{"type":"boolean","default":true}}}`, "", "additionalProperties"},
		{"no default", leaf(`{"type":"boolean"}`), "", "needs a `default`"},
		{"default off schema", leaf(`{"type":"string","maxLength":2,"default":"toolong"}`), "", "defaults don't satisfy"},
		{"secret default", leaf(`{"type":"string","default":"secret:main/key"}`), "", "secret:"},
		{"ref", leaf(`{"$ref":"#/$defs/x"}`), "", "$ref"},
		{"dotted key", `{"type":"object","additionalProperties":false,"properties":{"a.b":{"type":"boolean","default":true}}}`, "", "isn't a valid key"},
		{"root not object", `{"type":"boolean","default":true}`, "", "root"},
		{"too big", leaf(`{"type":"string","default":"` + strings.Repeat("x", AppConfigMaxBytes) + `"}`), "", "more than the"},
		{"unknown path", appConfigSchema, "read:\n  nope: \"true\"\n", "isn't declared"},
		{"bad rule", appConfigSchema, "read:\n  beta: hasRole(user, \"ghost\")\n", "not declared in realm.yaml"},
		{"no guard", appConfigSchema, "read:\n  beta: user.email == \"a\"\n", "guard"},
		{"document", appConfigSchema, "read:\n  beta: document.x == 1\n", "not available in app configuration rules"},
		{"unknown key", appConfigSchema, "write:\n  beta: \"true\"\n", "write"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTree(t, appConfigTree(tc.schema, tc.rules)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestAppConfigNeedsAuth(t *testing.T) {
	files := appConfigTree(appConfigSchema, "")
	files["shop/realm.yaml"] = "auth: disabled\n"
	_, err := Load(writeTree(t, files))
	if err == nil || !strings.Contains(err.Error(), "auth enabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppConfigIsPartOfTheFingerprint(t *testing.T) {
	fp := func(rules string) string {
		reg, err := Load(writeTree(t, appConfigTree(appConfigSchema, rules)))
		if err != nil {
			t.Fatal(err)
		}
		f, err := reg.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a, b := fp(appConfigRules), fp("read:\n  maintenance: \"true\"\n")
	if a == b {
		t.Error("rules.yaml doesn't change the fingerprint")
	}
	if fp(appConfigRules) != a {
		t.Error("fingerprint isn't stable")
	}
}
