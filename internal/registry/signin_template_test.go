package registry

import (
	"os"
	"strings"
	"testing"
)

// uncommented returns the lines of the template between two markers with their "# " removed.
func uncommented(t *testing.T, text, from, to string) string {
	t.Helper()
	i := strings.Index(text, from)
	j := strings.Index(text[i+1:], to)
	if i < 0 || j < 0 {
		t.Fatalf("the template has no %q ... %q", from, to)
	}
	var out []string
	for _, line := range strings.Split(text[i:i+1+j], "\n") {
		if rest, ok := strings.CutPrefix(line, "  # "); ok && !strings.HasPrefix(rest, " ") || strings.HasPrefix(line, "  #   ") {
			out = append(out, "  "+strings.TrimPrefix(strings.TrimPrefix(line, "  #"), " "))
		}
	}
	return strings.Join(out, "\n")
}

// What `backd template realm` writes about signing in is the configuration as it is read: with the
// comments removed, the providers and the sign-in section load without a complaint.
func TestTemplateSignInSectionsLoad(t *testing.T) {
	data, err := os.ReadFile("../templates/files/realm.yaml")
	if err != nil {
		t.Skip("the template is not here:", err)
	}
	text := string(data)
	block := uncommented(t, text, "  # sign_in:\n", "  # Two-step sign-in")
	yaml := "auth:\n  strategies:\n    password: [password]\n  signup: open\n" + block + "\n"
	s, errs := parseRealmSettings([]byte(yaml))
	if len(errs) > 0 {
		t.Fatalf("the sign_in and providers of the template: %v\n%s", errs, yaml)
	}
	for _, name := range BuiltinProviders {
		if s.Providers[name] == nil {
			t.Errorf("the template doesn't show %s", name)
		}
	}
	// Every field a kind takes appears in the template, so nobody has to read the code to find one.
	for _, spec := range kindSpecs {
		for _, field := range spec.fields {
			if !strings.Contains(text, field+":") {
				t.Errorf("the template never shows %q", field)
			}
		}
	}
}

// The guide of the providers names every field a kind takes, and the reference of realm.yaml the
// keys of organizations.sso.
func TestDocsNameTheSignInFields(t *testing.T) {
	guide, err := os.ReadFile("../../docs/content/docs/build/auth/providers.md")
	if err != nil {
		t.Skip("the docs are not here:", err)
	}
	for name, spec := range kindSpecs {
		for _, field := range spec.fields {
			if !strings.Contains(string(guide), field) {
				t.Errorf("docs/content/docs/build/auth/providers.md never names %q (a field of %s)", field, name)
			}
		}
	}
	ref, err := os.ReadFile("../../docs/content/docs/build/configuration/realm.md")
	if err != nil {
		t.Skip("the docs are not here:", err)
	}
	for _, key := range []string{"max_per_org", "providers", "sign_in", "allowed_redirects", "app_url"} {
		if !strings.Contains(string(ref), key) {
			t.Errorf("realm.md never names %q", key)
		}
	}
}
