package templates

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

// TestSampleMatchesExample keeps the embedded sample database identical to
// the blog realm's database in examples/config. The example realm.yaml is
// tailored for the example app, so it only has to load.
func TestSampleMatchesExample(t *testing.T) {
	root := t.TempDir()
	if _, err := Realm(root, "blog", true); err != nil {
		t.Fatal(err)
	}
	got, want := readTree(t, filepath.Join(root, "blog", "main")), readTree(t, "../../examples/config/blog/main")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("generated sample differs from examples/config/blog/main; update one to match the other\ngot files:  %v\nwant files: %v", keys(got), keys(want))
	}
	for name, content := range got {
		if content != want[name] {
			t.Errorf("%s differs from examples/config/blog/main/%s", name, name)
		}
	}
	reg, err := registry.Load("../../examples/config")
	if err != nil {
		t.Fatalf("examples/config doesn't load: %v", err)
	}
	if s := reg.Realms["blog"].Settings; s.Signup != registry.SignupOpen {
		t.Errorf("the example blog realm must allow sign-up for the example app: %+v", s)
	}
}

// emailFiles are the default email templates `template realm` writes, in order.
func emailFiles(realm string, created bool) []File {
	var out []File
	for _, kind := range email.SystemKinds {
		for _, name := range []string{"en.html", "en.subject.txt", "en.txt"} {
			out = append(out, File{Path: realm + "/email/" + kind + "/" + name, Created: created})
		}
	}
	for _, kind := range email.PageKinds {
		out = append(out, File{Path: realm + "/pages/" + kind + "/en.html", Created: created})
	}
	return out
}

func TestRealm(t *testing.T) {
	root := t.TempDir()
	files, err := Realm(root, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]File{{Path: "demo/realm.yaml", Created: true}}, emailFiles("demo", true)...); !reflect.DeepEqual(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}

	// Existing files are never overwritten.
	custom := filepath.Join(root, "demo", "realm.yaml")
	if err := os.WriteFile(custom, []byte("signup: open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err = Realm(root, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]File{{Path: "demo/realm.yaml"}}, emailFiles("demo", false)...)
	want = append(want, []File{
		{Path: "demo/main/_functions/deno.json", Created: true},
		{Path: "demo/main/_functions/stats/function.yaml", Created: true},
		{Path: "demo/main/_functions/stats/index.ts", Created: true},
		{Path: "demo/main/posts/indexes.json", Created: true},
		{Path: "demo/main/posts/rules.yaml", Created: true},
		{Path: "demo/main/posts/schema.json", Created: true},
	}...)
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
	if data, _ := os.ReadFile(custom); string(data) != "signup: open\n" {
		t.Errorf("realm.yaml was overwritten: %q", data)
	}

	// The generated config loads.
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := reg.Collection("demo", "main", "posts"); !ok || c.Rules == nil {
		t.Error("demo/main/posts not loaded with its rules")
	}
}

func TestDatabase(t *testing.T) {
	root := t.TempDir()
	if _, err := Realm(root, "demo", false); err != nil {
		t.Fatal(err)
	}
	files, err := Database(root, "demo", "cms", false)
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "demo", "cms")); err != nil || !fi.IsDir() {
		t.Fatalf("database directory not created: %v", err)
	}
	files, err = Database(root, "demo", "blog", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 6 || files[5].Path != "demo/blog/posts/schema.json" || !files[5].Created {
		t.Errorf("files = %v", files)
	}
	if _, err := registry.Load(root); err != nil {
		t.Fatal(err)
	}
}

func TestProject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "my-app")
	files, err := Project(dir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"config/shop/realm.yaml",
		"config/shop/main/posts/indexes.json",
		"config/shop/main/posts/rules.yaml",
		"config/shop/main/posts/schema.json",
		"config/shop/main/_functions/deno.json",
		"config/shop/main/_functions/lib/testing.js",
		"config/shop/main/_functions/stats/function.yaml",
		"config/shop/main/_functions/stats/index.ts",
		"config/shop/main/_functions/stats/index.test.ts",
		"config/shop/email/verify-email/en.txt",
		"config/shop/email/invitation/en.html",
		"Dockerfile",
		"Dockerfile.executor",
		"compose.yaml",
		".gitignore",
		".github/workflows/ci.yml",
		"README.md",
	}
	got := map[string]bool{}
	for _, f := range files {
		if !f.Created {
			t.Errorf("%s reported unchanged on a fresh project", f.Path)
		}
		got[f.Path] = true
	}
	for _, p := range wantPaths {
		if !got[p] {
			t.Errorf("missing %s", p)
		}
	}
	if want := len(wantPaths) + len(email.SystemKinds)*3 + len(email.PageKinds) - 2; len(got) != want { // the two email files listed above are among the 24
		t.Errorf("got %d files, want %d: %v", len(got), want, files)
	}

	// The generated config loads and its README names the actual realm
	// and database, not the raw placeholders.
	if _, err := registry.Load(filepath.Join(dir, "config")); err != nil {
		t.Fatalf("generated config doesn't load: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "__REALM__") || strings.Contains(string(readme), "__DATABASE__") {
		t.Errorf("README.md still has unreplaced placeholders:\n%s", readme)
	}
	if !strings.Contains(string(readme), "shop") || !strings.Contains(string(readme), "main") {
		t.Errorf("README.md doesn't name the realm/database:\n%s", readme)
	}

	// Refuses a non-empty target, and an invalid realm name.
	if _, err := Project(dir, "shop"); err == nil || !strings.Contains(err.Error(), "already exists and isn't an empty directory") {
		t.Errorf("existing dir: %v", err)
	}
	if _, err := Project(filepath.Join(root, "other"), "Shop"); err == nil || !strings.Contains(err.Error(), `invalid realm name "Shop"`) {
		t.Errorf("bad realm name: %v", err)
	}
}

// TestProjectBuildsAndChecks bundles the generated project's sample
// function with real Deno and runs the same startup check `backd serve`
// would, proving the template isn't just well-formed YAML/JSON but an
// actually loadable, buildable config.
func TestProjectBuildsAndChecks(t *testing.T) {
	bin := os.Getenv("DENO")
	if bin == "" {
		bin = "deno"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("deno not found; skipping (runs in the dockerized test image)")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "my-app")
	if _, err := Project(dir, "shop"); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := functions.Build(context.Background(), reg, functions.Options{Deno: bin, Check: true}); err != nil {
		t.Fatalf("functions build --check: %v", err)
	}
	reg, err = registry.Load(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckBundles(); err != nil {
		t.Errorf("CheckBundles: %v", err)
	}
}

func TestErrors(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		fn   func() ([]File, error)
		want string
	}{
		{"realm name", func() ([]File, error) { return Realm(root, "Demo", false) }, `invalid realm name "Demo"`},
		{"missing config dir", func() ([]File, error) { return Realm(filepath.Join(root, "nope"), "demo", false) }, "is not an existing directory"},
		{"database name", func() ([]File, error) { return Database(root, "demo", "_sys", false) }, `invalid database name "_sys"`},
		{"missing realm", func() ([]File, error) { return Database(root, "ghost", "app", false) }, "`backd template realm --realm ghost`"},
		{"too long", func() ([]File, error) { return Database(root, strings.Repeat("r", 32), strings.Repeat("d", 32), false) }, "shorter than 64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.fn(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".build" {
				return filepath.SkipDir // function bundles, built locally and gitignored
			}
			return nil
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// email-capture is a delivery function and the outbox it writes to; with the
// realm pointed at it, the generated config loads.
func TestEmailCapture(t *testing.T) {
	root := t.TempDir()
	if _, err := Realm(root, "demo", false); err != nil {
		t.Fatal(err)
	}
	if _, err := EmailCapture(root, "demo", "notify"); err == nil || !strings.Contains(err.Error(), "backd template database") {
		t.Errorf("without the database: %v", err)
	}
	if _, err := Database(root, "demo", "notify", false); err != nil {
		t.Fatal(err)
	}
	files, err := EmailCapture(root, "demo", "notify")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"demo/notify/_functions/deno.json", "demo/notify/_functions/email-capture/function.yaml", "demo/notify/_functions/email-capture/index.ts",
		"demo/notify/outbox/schema.json", "demo/notify/outbox/rules.yaml", "demo/notify/outbox/indexes.json",
	}
	for i, f := range files {
		if f.Path != want[i] || !f.Created {
			t.Errorf("file %d = %+v, want %s created", i, f, want[i])
		}
	}
	if again, _ := EmailCapture(root, "demo", "notify"); again[1].Created {
		t.Error("a second run overwrote the function")
	}
	index, _ := os.ReadFile(filepath.Join(root, "demo/notify/_functions/email-capture/index.ts"))
	fnYAML, _ := os.ReadFile(filepath.Join(root, "demo/notify/_functions/email-capture/function.yaml"))
	if !strings.Contains(string(index), `db("notify")`) || strings.Contains(string(index), "__DATABASE__") || !strings.Contains(string(fnYAML), "function: notify/email-capture") {
		t.Errorf("the database name wasn't filled in:\n%s\n%s", index, fnYAML)
	}

	// Without email configured it loads; pointing the realm at it works too.
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatalf("the template doesn't load: %v", err)
	}
	fn := reg.Realms["demo"].Databases["notify"].Functions.Functions["email-capture"]
	if !fn.Internal || !fn.DevOnly || fn.Mode != registry.ModeAsync || !fn.Admin {
		t.Errorf("email-capture: %+v", fn)
	}
	realm := filepath.Join(root, "demo", "realm.yaml")
	data, _ := os.ReadFile(realm)
	os.WriteFile(realm, append(data, []byte("\nemail:\n  function: notify/email-capture\n  from: dev@example.com\n  public_url: http://localhost:8080\n")...), 0o644)
	if _, err := registry.Load(root); err != nil {
		t.Fatalf("a realm that sends through email-capture doesn't load: %v", err)
	}
}

func TestCollectionPolicy(t *testing.T) {
	root := t.TempDir()
	if _, err := Realm(root, "demo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectionPolicy(root, "demo", "main", "nothing"); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("an unknown collection: %v", err)
	}
	files, err := CollectionPolicy(root, "demo", "main", "posts")
	if err != nil || len(files) != 1 || files[0].Path != "demo/main/posts/collection.yaml" || !files[0].Created {
		t.Fatalf("files: %+v, %v", files, err)
	}
	if again, _ := CollectionPolicy(root, "demo", "main", "posts"); again[0].Created {
		t.Error("a second run overwrote the file")
	}
	// Everything in it is commented out: the realm still loads, with no policy.
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatalf("the generated config doesn't load: %v", err)
	}
	if c, _ := reg.Collection("demo", "main", "posts"); c.Erasure != nil {
		t.Errorf("the template must declare no policy: %+v", c.Erasure)
	}
}
