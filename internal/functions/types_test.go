package functions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

func TestJSDocTypes(t *testing.T) {
	root := t.TempDir()
	fn := "shop/app/" + registry.FunctionsDir + "/"
	write(t, root, map[string]string{
		"shop/realm.yaml":             "",
		fn + "checkout/function.yaml": "",
		fn + "checkout/index.js":      "",
		fn + "checkout/input.schema.json": `{
			"type": "object",
			"required": ["cart"],
			"properties": {
				"cart": {"type": "string"},
				"note": {"type": "string"},
				"quantity": {"type": "integer"}
			}
		}`,
		fn + "checkout/output.schema.json": `{
			"type": "object",
			"required": ["order", "total"],
			"properties": {
				"order": {"type": "string"},
				"total": {"type": "number"},
				"items": {"type": "array", "items": {"type": "string"}}
			}
		}`,
		fn + "hello/function.yaml": "", // no schemas: nothing generated for it
		fn + "hello/index.js":      "",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out := JSDocTypes(reg)

	if !strings.Contains(out, "shop/app/checkout") {
		t.Errorf("missing function header:\n%s", out)
	}
	if strings.Contains(out, "shop/app/hello") {
		t.Errorf("a function without schemas shouldn't appear:\n%s", out)
	}
	if !strings.Contains(out, `@typedef {{"cart": string, "note"`) {
		t.Errorf("no input typedef:\n%s", out)
	}
	// Required fields have no trailing "=": optional ones do.
	if !strings.Contains(out, `"cart": string,`) || !strings.Contains(out, `"note": string=`) || !strings.Contains(out, `"quantity": number=`) {
		t.Errorf("required/optional not distinguished:\n%s", out)
	}
	if !strings.Contains(out, `"order": string,`) || !strings.Contains(out, `"total": number}`) || !strings.Contains(out, `"items": Array<string>=`) {
		t.Errorf("output typedef wrong:\n%s", out)
	}
	if !strings.Contains(out, "ShopAppCheckoutInput") || !strings.Contains(out, "ShopAppCheckoutOutput") {
		t.Errorf("typedef names not built from realm/database/function:\n%s", out)
	}
}

func TestJSDocTypesEmpty(t *testing.T) {
	root := t.TempDir()
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if out := JSDocTypes(reg); !strings.Contains(out, "no function declares") {
		t.Errorf("empty config: %q", out)
	}
}

func TestJSDocType(t *testing.T) {
	for _, tt := range []struct {
		name   string
		schema string
		want   string
	}{
		{"string", `{"type": "string"}`, "string"},
		{"number", `{"type": "integer"}`, "number"},
		{"boolean", `{"type": "boolean"}`, "boolean"},
		{"nullable union", `{"type": ["string", "null"]}`, "string|null"},
		{"enum", `{"enum": ["a", "b"]}`, `"a"|"b"`},
		{"array", `{"type": "array", "items": {"type": "string"}}`, "Array<string>"},
		{"array no items", `{"type": "array"}`, "Array<*>"},
		{"object no properties", `{"type": "object"}`, "Object<string, *>"},
		{"unknown", `{}`, "*"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var schema any
			if err := json.Unmarshal([]byte(tt.schema), &schema); err != nil {
				t.Fatal(err)
			}
			if got := jsdocType(schema, 0); got != tt.want {
				t.Errorf("jsdocType(%s) = %q, want %q", tt.schema, got, tt.want)
			}
		})
	}
}

func TestPascal(t *testing.T) {
	for in, want := range map[string]string{
		"shop":      "Shop",
		"my-app":    "MyApp",
		"my_app":    "MyApp",
		"checkout2": "Checkout2",
	} {
		if got := pascal(in); got != want {
			t.Errorf("pascal(%q) = %q, want %q", in, got, want)
		}
	}
}
