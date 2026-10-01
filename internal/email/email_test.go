package email

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDefaults writes the default English templates of every system kind
// under dir, as `backd template realm` does.
func writeDefaults(t *testing.T, dir string) {
	t.Helper()
	for _, kind := range SystemKinds {
		files, err := DefaultFiles(kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, kind), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, kind, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDefaultsLoadAndRender(t *testing.T) {
	dir := t.TempDir()
	writeDefaults(t, dir)
	tpl, errs := Load(dir, []string{"en"})
	if len(errs) > 0 {
		t.Fatalf("defaults don't load: %v", errs)
	}
	if got := tpl.Kinds(); len(got) != len(SystemKinds) {
		t.Errorf("kinds = %v", got)
	}
	msg, err := tpl.Render(VerifyEmail, "en", Data{Realm: "blog", User: User{Email: "ana@example.com"}, Link: "https://x.example/v1/blog/_auth/verify-email?token=abc", ExpiresAt: time.Date(2026, 10, 3, 12, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "Confirm your email address for blog" || !strings.Contains(msg.Text, "?token=abc") || !strings.Contains(msg.HTML, `href="https://x.example/v1/blog/_auth/verify-email?token=abc"`) || !strings.Contains(msg.Text, "2026-10-03 12:30 UTC") {
		t.Errorf("message = %+v", msg)
	}
	// HTML escapes what text doesn't: an address with markup can't inject any.
	msg, _ = tpl.Render(PasswordChanged, "en", Data{Realm: "blog", User: User{Email: "<b>x</b>@example.com"}})
	if strings.Contains(msg.HTML, "<b>x</b>") || !strings.Contains(msg.Text, "<b>x</b>") {
		t.Errorf("escaping: %q / %q", msg.HTML, msg.Text)
	}
}

func TestLoadProblems(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(t *testing.T, dir string)
		want   string
	}{
		{"required kind missing", func(t *testing.T, dir string) { os.RemoveAll(filepath.Join(dir, ResetPassword)) }, `the email kind "reset-password" has no template`},
		{"locale missing", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, VerifyEmail, "es.html")) }, "es.html: missing"},
		{"syntax", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, VerifyEmail, "en.txt"), []byte("{{.Link"), 0o644)
		}, "en.txt"},
		{"unknown field", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, VerifyEmail, "en.subject.txt"), []byte("{{.Nope}}"), 0o644)
		}, "Nope"},
		{"bad kind name", func(t *testing.T, dir string) { os.MkdirAll(filepath.Join(dir, "Bad Kind"), 0o755) }, "invalid email kind name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDefaults(t, dir)
			// A Spanish copy of every kind, so "es" can be listed.
			for _, kind := range SystemKinds {
				for _, n := range []string{"subject.txt", "txt", "html"} {
					data, _ := os.ReadFile(filepath.Join(dir, kind, "en."+n))
					os.WriteFile(filepath.Join(dir, kind, "es."+n), data, 0o644)
				}
			}
			tt.mutate(t, dir)
			_, errs := Load(dir, []string{"en", "es"})
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			if !strings.Contains(strings.Join(all, "\n"), tt.want) {
				t.Errorf("errors %v, want one containing %q", all, tt.want)
			}
		})
	}
	if _, errs := Load(filepath.Join(t.TempDir(), "nowhere"), []string{"en"}); len(errs) != 1 || !strings.Contains(errs[0].Error(), "has no email folder") {
		t.Errorf("missing folder: %v", errs)
	}
}

func TestPurposes(t *testing.T) {
	for kind, want := range map[string]Purpose{VerifyEmail: "verify-email", ResetPassword: "reset-password", ChangeEmail: "change-email", EmailChanged: "revert-email-change", Invitation: "invitation", Welcome: "", AccountExists: "", PasswordChanged: ""} {
		if got := TokenPurpose(kind); got != want {
			t.Errorf("TokenPurpose(%s) = %q, want %q", kind, got, want)
		}
	}
	if Purpose("change-email").LinkPath() != "confirm-email-change" || Purpose("invitation").LinkPath() != "accept-invitation" || Purpose("verify-email").LinkPath() != "verify-email" {
		t.Error("link paths")
	}
	if !ValidLocale("es") || !ValidLocale("es-MX") || ValidLocale("EN") || ValidLocale("english") {
		t.Error("ValidLocale")
	}
}
