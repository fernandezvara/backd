package registry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAuditEntriesDefaultsAndOverrides(t *testing.T) {
	s, errs := parseRealmSettings([]byte(authBase + "audit:\n  entries:\n    role.add: false\n    user.login: true\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for action, want := range map[string]bool{
		"role.add":             false, // switched off
		"user.login":           true,  // switched on
		"role.remove":          true,  // default: on
		"user.login_failed":    true,
		"admin.login":          true,
		AuditEntriesChanged:    true, // not configurable: always recorded
		"an.unknown.action":    true, // never silently dropped
		"passkey.login_failed": true,
	} {
		if got := s.AuditEnabled(action); got != want {
			t.Errorf("AuditEnabled(%q) = %v, want %v", action, got, want)
		}
	}
	d, _ := parseRealmSettings([]byte(authBase))
	if d.AuditEnabled("user.login") {
		t.Error("user.login is on by default; it must be off")
	}
}

// TestEveryRecordedAuditActionIsInTheTable is what makes `audit.entries` cover actions added
// later: an action recorded anywhere in the server that is not in AuditActions fails here.
func TestEveryRecordedAuditActionIsInTheTable(t *testing.T) {
	constRe := regexp.MustCompile(`\bAudit[A-Za-z]+\s*=\s*"([a-z][a-z_.]*)"`)
	callRe := regexp.MustCompile(`\.Audit\(\s*[\w.()]+,\s*"([a-z][a-z_.]*)"|\.AuditAs\(\s*[\w.()]+,\s*[^,()]+,\s*"([a-z][a-z_.]*)"`)
	found := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range constRe.FindAllStringSubmatch(string(data), -1) {
			found[m[1]] = path
		}
		for _, m := range callRe.FindAllStringSubmatch(string(data), -1) {
			found[m[1]+m[2]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Constants that are named like actions but are not audit actions.
	delete(found, "audit")
	delete(found, AuditEntriesChanged)
	if len(found) < 100 {
		t.Fatalf("found only %d actions in the source: the scan is broken", len(found))
	}
	for action, where := range found {
		if !IsAuditAction(action) {
			t.Errorf("%s records %q, which is not in registry.AuditActions: add it, so audit.entries can switch it", where, action)
		}
	}
	for _, a := range AuditActions {
		if _, ok := found[a.Name]; !ok && !strings.HasPrefix(a.Name, "user.login") && a.Name != "user.step_failed" {
			t.Errorf("%q is in registry.AuditActions but the code never records it", a.Name)
		}
	}
	seen := map[string]bool{}
	for _, a := range AuditActions {
		if seen[a.Name] {
			t.Errorf("%q is listed twice", a.Name)
		}
		seen[a.Name] = true
	}
}
