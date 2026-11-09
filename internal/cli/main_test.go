package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/email"
)

func TestRun(t *testing.T) {
	missingDir := map[string]string{"CONFIG_DIR": "/nonexistent", "MONGO_URI": "mongodb://localhost"}
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		wantOut  string
	}{
		{"no arguments print the help and start nothing", nil, missingDir, 0, "Usage: backd <command>"},
		{"invalid settings", []string{"serve"}, nil, 1, "CONFIG_DIR is required"},
		{"invalid config", []string{"serve"}, missingDir, 1, "invalid config"},
		{"provision", []string{"provision"}, missingDir, 1, "backd provision:"},
		{"help", []string{"help"}, nil, 0, "Usage: backd <command>"},
		{"help lists version", []string{"help"}, nil, 0, "print the version"},
		{"unknown command", []string{"nope"}, nil, 2, `unknown command: "nope"`},
		{"extra arguments", []string{"serve", "x"}, nil, 2, "unexpected arguments"},
		{"serve with-worker parses", []string{"serve", "--with-worker"}, missingDir, 1, "invalid config"},
		{"worker no config", []string{"worker"}, nil, 1, "CONFIG_DIR is required"},
		{"worker extra arguments", []string{"worker", "x"}, nil, 2, "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			var stderr bytes.Buffer
			if code := Run(tt.args, getenv, nil, &stderr, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.wantOut) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantOut)
			}
		})
	}
}

func TestRunTemplate(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"CONFIG_DIR": root}
	getenv := func(k string) string { return env[k] }

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"realm", []string{"template", "realm", "--realm", "demo"}, 0, "created   demo/realm.yaml\n" + emailLines("demo", "created   ", ""), ""},
		{"realm again", []string{"template", "realm", "--realm", "demo"}, 0, "exists    demo/realm.yaml (left unchanged)\n" + emailLines("demo", "exists    ", " (left unchanged)"), ""},
		{"database with sample", []string{"template", "database", "--realm", "demo", "--database", "cms", "--sample"}, 0,
			"database  demo/cms/\ncreated   demo/cms/_functions/deno.json\ncreated   demo/cms/_functions/stats/function.yaml\ncreated   demo/cms/_functions/stats/index.ts\ncreated   demo/cms/posts/collection.yaml\ncreated   demo/cms/posts/indexes.json\ncreated   demo/cms/posts/rules.test.yaml\ncreated   demo/cms/posts/schema.json\n", ""},
		{"collection policy", []string{"template", "collection-policy", "--realm", "demo", "--database", "cms", "--collection", "posts"}, 0, "exists    demo/cms/posts/collection.yaml (left unchanged)\n", ""},
		{"collection policy of nothing", []string{"template", "collection-policy", "--realm", "demo", "--database", "cms", "--collection", "ghost"}, 1, "", "doesn't exist"},
		{"missing realm", []string{"template", "database", "--realm", "ghost", "--database", "app"}, 1, "", "backd template realm --realm ghost"},
		{"missing database", []string{"template", "database", "--realm", "demo"}, 2, "", "--database"},
		{"bad kind", []string{"template", "collection", "x"}, 2, "", `unknown command "collection"`},
		{"missing target", []string{"template", "realm"}, 2, "", "--realm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, getenv, nil, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if stdout.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantOut)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "cms", "posts", "schema.json")); err != nil {
		t.Error(err)
	}

	var stderr bytes.Buffer
	if code := Run([]string{"template", "realm", "--realm", "x"}, func(string) string { return "" }, nil, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "CONFIG_DIR is required") {
		t.Errorf("without CONFIG_DIR: code %d, stderr %q", code, stderr.String())
	}
}

func TestRunDatabases(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		"blog/realm.yaml":               "",
		"blog/main/posts/schema.json":   `{}`,
		"blog/cms/pages/schema.json":    `{}`,
		"shop/realm.yaml":               "auth: disabled\n",
		"shop/orders/items/schema.json": `{}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	getenv := func(k string) string { return map[string]string{"CONFIG_DIR": root}[k] }
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"databases"}, getenv, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, stderr.String())
	}
	// The system database only exists for realms with auth enabled; the
	// deployment database holds the provisioned config fingerprints.
	if want := "backd___deployment\nblog___system\nblog__cms\nblog__main\nshop__orders\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}

	stdout.Reset()
	if code := Run([]string{"databases", "--collections"}, getenv, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("--collections: exit code %d, stderr %q", code, stderr.String())
	}
	want := "backd___deployment.realms\nblog___system.api_keys\nblog___system.app_config\nblog___system.app_config_history\nblog___system.audit\nblog___system.auth_challenges\nblog___system.consent_records\nblog___system.data_keys\nblog___system.devices\nblog___system.email_tokens\nblog___system.file_deletions\nblog___system.file_journal\nblog___system.idempotency\nblog___system.identities\nblog___system.invitations\nblog___system.invocations\nblog___system.jobs\nblog___system.login_attempts\nblog___system.login_codes\nblog___system.mfa\nblog___system.oauth_states\nblog___system.org_connections\nblog___system.org_invitations\nblog___system.org_member_seq\nblog___system.org_members\nblog___system.org_teams\nblog___system.org_templates\nblog___system.orgs\nblog___system.push_outbox\nblog___system.push_sends\nblog___system.schedules\nblog___system.schema_checks\nblog___system.secrets\nblog___system.security_state\n" +
		"blog___system.sessions\nblog___system.storage_usage\nblog___system.user_exports\nblog___system.users\nblog__cms.pages\nblog__main.posts\nshop__orders.items\n"
	if stdout.String() != want {
		t.Errorf("--collections: stdout = %q, want %q", stdout.String(), want)
	}

	for _, tt := range []struct {
		args    []string
		env     map[string]string
		code    int
		wantErr string
	}{
		{[]string{"databases", "x"}, map[string]string{"CONFIG_DIR": root}, 2, "unexpected arguments"},
		{[]string{"databases"}, nil, 1, "CONFIG_DIR is required"},
		{[]string{"databases"}, map[string]string{"CONFIG_DIR": "/nonexistent"}, 1, "invalid config"},
	} {
		stderr.Reset()
		if code := Run(tt.args, func(k string) string { return tt.env[k] }, nil, io.Discard, &stderr); code != tt.code || !strings.Contains(stderr.String(), tt.wantErr) {
			t.Errorf("%v: code %d, stderr %q; want %d and %q", tt.args, code, stderr.String(), tt.code, tt.wantErr)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var stdout bytes.Buffer
		if code := Run([]string{arg}, func(string) string { return "" }, nil, &stdout, io.Discard); code != 0 || stdout.String() != "backd dev\n" {
			t.Errorf("%s: code %d, output %q", arg, code, stdout.String())
		}
	}
}

// emailLines are the lines `template realm` prints for the default email
// templates: one per file, kinds in order.
func emailLines(realm, prefix, suffix string) string {
	var b strings.Builder
	for _, kind := range email.SystemKinds {
		for _, name := range []string{"en.html", "en.subject.txt", "en.txt"} {
			b.WriteString(prefix)
			b.WriteString(realm)
			b.WriteString("/_realm/email/")
			b.WriteString(kind)
			b.WriteString("/")
			b.WriteString(name)
			b.WriteString(suffix)
			b.WriteString("\n")
		}
	}
	for _, kind := range append(slices.Clone(email.PageKinds), email.OptionalPageKinds...) {
		b.WriteString(prefix)
		b.WriteString(realm)
		b.WriteString("/_realm/pages/")
		b.WriteString(kind)
		b.WriteString("/en.html")
		b.WriteString(suffix)
		b.WriteString("\n")
	}
	b.WriteString(prefix)
	b.WriteString(realm)
	b.WriteString("/_realm/compliance/readme/en.html")
	b.WriteString(suffix)
	b.WriteString("\n")
	b.WriteString(prefix)
	b.WriteString(realm)
	b.WriteString("/_realm/push/example/en.yaml")
	b.WriteString(suffix)
	b.WriteString("\n")
	return b.String()
}
