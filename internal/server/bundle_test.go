package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestStaleFunctions(t *testing.T) {
	fn := func(db string) map[string]string {
		return map[string]string{
			"app/" + db + "/notes/schema.json":              `{}`,
			"app/" + db + "/_functions/hello/function.yaml": "mode: sync\n",
			"app/" + db + "/_functions/hello/index.ts":      "export default () => 1;\n",
		}
	}
	files := map[string]string{"app/realm.yaml": "auth: disabled\n", "app/plain/notes/schema.json": `{}`}
	for _, db := range []string{"fresh", "stale", "missing"} {
		for k, v := range fn(db) {
			files[k] = v
		}
	}
	root := cmdtest.Write(t, files)
	cmdtest.WriteFuncManifest(t, filepath.Join(root, "app/fresh/_functions"), "hello")
	cmdtest.WriteFuncManifest(t, filepath.Join(root, "app/stale/_functions"), "hello")
	// An edit after the build makes the bundle stale.
	if err := os.WriteFile(filepath.Join(root, "app/stale/_functions/hello/index.ts"), []byte("export default () => 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := staleFunctions(reg)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app/missing", "app/stale"}; !slices.Equal(got, want) {
		t.Errorf("stale = %v, want %v (fresh bundles and databases without functions are not stale)", got, want)
	}
}

// With every bundle fresh nothing is built: Deno is not even looked for.
func TestBuildStaleFunctionsSkipsFreshBundles(t *testing.T) {
	root := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                          "auth: disabled\n",
		"app/main/notes/schema.json":              `{}`,
		"app/main/_functions/hello/function.yaml": "mode: sync\n",
		"app/main/_functions/hello/index.ts":      "export default () => 1;\n",
	})
	cmdtest.WriteFuncManifest(t, filepath.Join(root, "app/main/_functions"), "hello")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := buildStaleFunctions(context.Background(), reg, "/nonexistent/deno", log); err != nil {
		t.Errorf("fresh bundles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "app/main/_functions/hello/index.ts"), []byte("export default () => 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := buildStaleFunctions(context.Background(), reg, "/nonexistent/deno", log); err == nil {
		t.Error("a stale bundle was not built (the missing Deno should have failed the build)")
	}
}

// The dev reloader rebuilds migration bundles too: a database whose migrations have no build,
// or an old one, is listed, and one built from the current sources is not.
func TestStaleMigrations(t *testing.T) {
	root := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                 "auth:\n  strategies:\n    password: [password]\n",
		"app/main/items/schema.v1.json":  `{}`,
		"app/main/items/schema.v2.json":  `{}`,
		"app/main/items/schema.v2.ts":    "export default (d: unknown) => d;\n",
		"app/other/notes/schema.v1.json": `{}`,
		"app/other/notes/schema.v2.json": `{}`,
		"app/other/notes/schema.v2.ts":   "export default (d: unknown) => d;\n",
		"app/plain/notes/schema.json":    `{}`,
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := staleFunctions(reg); !slices.Equal(got, []string{"app/main migrations", "app/other migrations"}) {
		t.Fatalf("stale without a build = %v", got)
	}
	// A manifest of the current sources makes main fresh.
	db := reg.Realms["app"].Databases["main"]
	src, _ := registry.MigrationSourceHash(db)
	if err := os.MkdirAll(db.MigrationsBuildDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"deno": "` + registry.DenoVersion + `", "source": "` + src + `", "collections": {}}`
	if err := os.WriteFile(filepath.Join(db.MigrationsBuildDir(), registry.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := staleFunctions(reg); !slices.Equal(got, []string{"app/other migrations"}) {
		t.Errorf("stale with main built = %v", got)
	}
	// An edit after the build makes it stale again.
	if err := os.WriteFile(filepath.Join(root, "app/main/items/schema.v2.ts"), []byte("export default (d: unknown) => ({ ...(d as object) });\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := staleFunctions(reg); !slices.Equal(got, []string{"app/main migrations", "app/other migrations"}) {
		t.Errorf("stale after an edit = %v", got)
	}
}

// The dev server keeps each versioned collection's schema.d.ts current.
func TestDevWritesSchemaFiles(t *testing.T) {
	root := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                "auth:\n  strategies:\n    password: [password]\n",
		"app/main/items/schema.v1.json": `{"type":"object","properties":{"title":{"type":"string"}}}`,
		"app/main/items/schema.v2.json": `{"type":"object","properties":{"title":{"type":"string"},"slug":{"type":"string"}}}`,
		"app/main/items/schema.v2.ts":   "export default (d: unknown) => d;\n",
		"app/main/plain/schema.json":    `{}`,
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	writeDevSchemaFiles(reg, log)
	data, err := os.ReadFile(filepath.Join(root, "app/main/items/schema.d.ts"))
	if err != nil || !strings.Contains(string(data), "export type Current = V2;") {
		t.Fatalf("schema.d.ts: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(root, "app/main/plain/schema.d.ts")); err == nil {
		t.Error("an unversioned collection got a schema.d.ts")
	}
	// Editing a version updates it on the next load.
	if err := os.WriteFile(filepath.Join(root, "app/main/items/schema.v2.json"), []byte(`{"type":"object","properties":{"title":{"type":"string"},"rank":{"type":"integer"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, _ = registry.Load(root)
	writeDevSchemaFiles(reg, log)
	if data, _ := os.ReadFile(filepath.Join(root, "app/main/items/schema.d.ts")); !strings.Contains(string(data), "rank?: number") {
		t.Errorf("schema.d.ts after an edit:\n%s", data)
	}
}
