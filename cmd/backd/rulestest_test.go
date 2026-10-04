package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rulesConfig writes a one-collection config with the given fixture.
func rulesConfig(t *testing.T, fixture string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "acme", "app", "posts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "acme", "realm.yaml"): "roles:\n  admin: {}\n",
		filepath.Join(dir, "schema.json"):         `{"type":"object","properties":{"title":{"type":"string"},"published":{"type":"boolean"}}}`,
		filepath.Join(dir, "rules.yaml"):          "read: document.published == true\n",
	}
	if fixture != "" {
		files[filepath.Join(dir, "rules.test.yaml")] = fixture
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runRules(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"CONFIG_DIR": root}
	code := run(append([]string{"rules", "test"}, args...), func(k string) string { return env[k] }, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRulesTest(t *testing.T) {
	good := "documents:\n  pub: {title: P, published: true}\n  draft: {title: D}\ntests:\n  - read: { allow: [pub], deny: [draft] }\n"
	code, out, errOut := runRules(t, rulesConfig(t, good))
	if code != 0 || !strings.Contains(out, "acme/app/posts: 2 assertions, ok") || !strings.Contains(out, "1 collections tested, 2 assertions passed, 0 failed") {
		t.Errorf("passing: %d\n%s%s", code, out, errOut)
	}
	// Verbose lists what passes.
	if _, out, _ = runRules(t, rulesConfig(t, good), "--verbose"); !strings.Contains(out, "✓ anonymous reads pub") {
		t.Errorf("verbose: %s", out)
	}

	// A failing assertion: named, explained, exit 1.
	bad := "documents:\n  pub: {title: P, published: true}\ntests:\n  - name: wrong\n    read: { deny: [pub] }\n"
	code, out, _ = runRules(t, rulesConfig(t, bad))
	if code != 1 || !strings.Contains(out, "1 FAILED") || !strings.Contains(out, "✗ anonymous reads pub [wrong]") || !strings.Contains(out, "expected deny, the rules allow it") {
		t.Errorf("failing: %d\n%s", code, out)
	}

	// A mistake in the fixture is not an assertion failure, but still fails.
	code, out, _ = runRules(t, rulesConfig(t, "tests:\n  - as: nobody\n    read: {}\n"))
	if code != 1 || !strings.Contains(out, "the fixture is wrong") || !strings.Contains(out, `"nobody" is not one of the fixture's users`) {
		t.Errorf("wrong fixture: %d\n%s", code, out)
	}

	// No fixture: listed; --strict fails.
	code, out, _ = runRules(t, rulesConfig(t, ""))
	if code != 0 || !strings.Contains(out, "acme/app/posts: has rules.yaml but no rules.test.yaml") {
		t.Errorf("untested: %d\n%s", code, out)
	}
	if code, _, errOut = runRules(t, rulesConfig(t, ""), "--strict"); code != 1 || !strings.Contains(errOut, "no tests (--strict)") {
		t.Errorf("--strict: %d %s", code, errOut)
	}

	// --collection filters, and a filter that matches nothing is an error.
	root := rulesConfig(t, good)
	if code, out, _ = runRules(t, root, "--collection", "acme/app"); code != 0 || !strings.Contains(out, "1 collections tested") {
		t.Errorf("--collection: %d %s", code, out)
	}
	if code, _, errOut = runRules(t, root, "--collection", "nope"); code != 1 || !strings.Contains(errOut, `no collection matches --collection "nope"`) {
		t.Errorf("--collection nope: %d %s", code, errOut)
	}
}

// The examples' fixtures pass: they document the format, and a rule change
// that breaks them is caught with the rest of the tests.
func TestExampleRulesFixtures(t *testing.T) {
	code, out, errOut := runRules(t, "../../examples/config")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"blog/main/posts:", "expenses/main/groups:"} {
		if !strings.Contains(out, want) || strings.Contains(out, want+" fixture") {
			t.Errorf("no result for %s in:\n%s", want, out)
		}
	}
}
