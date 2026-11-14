package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMigrationImports(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string // "" = accepted; else part of the error, including the line
	}{
		"type import":                     {"import type { V1, V2, Meta } from \"./schema.d.ts\";\nexport default (d: V1, m: Meta): V2 => ({ ...d });\n", ""},
		"single quotes":                   {"import type { V1 } from './schema.d.ts';\nexport default (d: V1) => d;\n", ""},
		"multi-line":                      {"import type {\n  V1,\n  V2,\n} from \"./schema.d.ts\";\nexport default (d: V1): V2 => d as V2;\n", ""},
		"no import":                       {"export default (d: unknown) => d;\n", ""},
		"in a comment":                    {"// import { x } from \"./lib.ts\";\n/* import y from \"z\" */\nexport default (d: unknown) => d;\n", ""},
		"in a template":                   {"export default (d: unknown) => `import x from \"y\"`;\n", ""},
		"a value import":                  {"import { V1 } from \"./schema.d.ts\";\nexport default (d: V1) => d;\n", ":1:"},
		"a type import of another module": {"export const a = 1;\nimport type { X } from \"./other.ts\";\nexport default (d: unknown) => d;\n", ":2:"},
		"a helper module":                 {"\n\nimport { cents } from \"../lib/money.ts\";\nexport default (d: unknown) => d;\n", ":3:"},
		"a package":                       {"import _ from \"npm:lodash\";\nexport default (d: unknown) => d;\n", ":1:"},
		"a bare import":                   {"import \"./side-effect.ts\";\nexport default (d: unknown) => d;\n", ":1:"},
		"a dynamic import":                {"export default async (d: unknown) => (await import(\"./x.ts\"), d);\n", ":1:"},
		"a re-export":                     {"export { x } from \"./x.ts\";\nexport default (d: unknown) => d;\n", ":1:"},
		"a type re-export":                {"export type { V1 } from \"./schema.d.ts\";\nexport default (d: unknown) => d;\n", ":1:"},
		"a namespace value":               {"import * as s from \"./schema.d.ts\";\nexport default (d: unknown) => d;\n", ":1:"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schema.v2.ts")
			if err := os.WriteFile(path, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			err := CheckMigrationImports(path)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), "schema.v2.ts"+tc.want)):
				t.Errorf("err = %v, want it to name the file and line %s", err, tc.want)
			}
		})
	}
}

func TestMigrationBundleCheck(t *testing.T) {
	two := map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": versionSchema, "schema.v2.ts": "export default (d: unknown) => d;"}
	root := writeTree(t, map[string]string{
		"shop/main/items/schema.v1.json": two["schema.v1.json"],
		"shop/main/items/schema.v2.json": two["schema.v2.json"],
		"shop/main/items/schema.v2.ts":   two["schema.v2.ts"],
		"shop/main/plain/schema.json":    versionSchema,
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	db := reg.Realms["shop"].Databases["main"]
	if got := db.MigratingCollections(); len(got) != 1 || got[0].Name != "items" {
		t.Fatalf("migrating = %v", got)
	}
	if err := reg.CheckBundles(); err == nil || !strings.Contains(err.Error(), "migrations are not built") {
		t.Fatalf("CheckBundles without a build = %v", err)
	}

	// A fake build: a bundle and its manifest.
	writeManifest := func(m MigrationManifest) {
		t.Helper()
		if err := os.MkdirAll(db.MigrationsBuildDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		data := `{"deno": "` + m.Deno + `", "source": "` + m.Source + `", "collections": {`
		if b, ok := m.Collections["items"]; ok {
			data += `"items": {"bundle": "` + b.Bundle + `", "sha256": "` + b.SHA256 + `"}`
		}
		data += "}}"
		if err := os.WriteFile(filepath.Join(db.MigrationsBuildDir(), ManifestFile), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, err := MigrationSourceHash(db)
	if err != nil {
		t.Fatal(err)
	}
	bundle := "export const steps = [];\n"
	good := MigrationManifest{Deno: DenoVersion, Source: src, Collections: map[string]ManifestBundle{}}
	writeBundle := func(content string) ManifestBundle {
		t.Helper()
		file := "items-test.js"
		if err := os.WriteFile(filepath.Join(db.MigrationsBuildDir(), file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return ManifestBundle{Bundle: file, SHA256: hashHex(content)}
	}
	os.MkdirAll(db.MigrationsBuildDir(), 0o755)
	good.Collections["items"] = writeBundle(bundle)
	writeManifest(good)
	if err := reg.CheckBundles(); err != nil {
		t.Fatalf("fresh build refused: %v", err)
	}

	for name, tc := range map[string]struct {
		change func()
		want   string
	}{
		"another Deno": {func() { m := good; m.Deno = "2.1.4"; writeManifest(m) }, "built with Deno 2.1.4"},
		"stale": {func() {
			os.WriteFile(filepath.Join(db.Dir, "items", "schema.v2.ts"), []byte("export default (d: unknown) => ({ ...(d as object) });"), 0o644)
		}, "stale"},
		"no bundle for the collection": {func() { m := good; m.Collections = map[string]ManifestBundle{}; writeManifest(m) }, `collection "items" has no migration bundle`},
		"a modified bundle": {func() {
			writeManifest(good)
			os.WriteFile(filepath.Join(db.MigrationsBuildDir(), "items-test.js"), []byte("tampered"), 0o644)
		}, "modified after the build"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.change()
			reg2, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := reg2.CheckBundles(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("CheckBundles = %v, want %q", err, tc.want)
			}
			// restore
			os.WriteFile(filepath.Join(db.Dir, "items", "schema.v2.ts"), []byte(two["schema.v2.ts"]), 0o644)
			good.Collections["items"] = writeBundle(bundle)
			writeManifest(good)
		})
	}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
