package registry

import (
	"strings"
	"testing"
)

func TestCustomIDSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml    string
		realm   string
		user    bool
		pattern string
		limit   int
	}{
		"off by default":    {"", "signup: open\n", false, "", 0},
		"a pattern":         {"custom_id:\n  pattern: 'INV-[0-9]{4}'\n", "signup: open\n", false, "INV-[0-9]{4}", DefaultDuplicateLimit},
		"a limit":           {"custom_id:\n  pattern: 'a+'\n  duplicate_limit: 5\n", "signup: open\n", false, "a+", 5},
		"the user shortcut": {"custom_id: user\n", "signup: open\n", true, "", DefaultDuplicateLimit},
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := softTree(t, tc.realm, tc.yaml, "")
			if err != nil {
				t.Fatal(err)
			}
			c, _ := reg.Collection("shop", "main", "orders")
			if tc.limit == 0 {
				if c.CustomID != nil {
					t.Fatalf("CustomID = %+v, want nil", c.CustomID)
				}
				return
			}
			if c.CustomID == nil || c.CustomID.User != tc.user || c.CustomID.Pattern != tc.pattern || c.CustomID.DuplicateLimit != tc.limit {
				t.Errorf("CustomID = %+v", c.CustomID)
			}
		})
	}
}

func TestCustomIDRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		realm, yaml, want string
	}{
		"no pattern":         {"signup: open\n", "custom_id: {}\n", "needs a pattern"},
		"an empty pattern":   {"signup: open\n", "custom_id:\n  pattern: ''\n", "needs a pattern"},
		"a broken pattern":   {"signup: open\n", "custom_id:\n  pattern: '(['\n", "RE2"},
		"a lookahead":        {"signup: open\n", "custom_id:\n  pattern: '(?=a)a'\n", "RE2"},
		"a long pattern":     {"signup: open\n", "custom_id:\n  pattern: '" + strings.Repeat("a", 513) + "'\n", "at most 512"},
		"a low limit":        {"signup: open\n", "custom_id:\n  pattern: a\n  duplicate_limit: 0\n", "between 1 and 1000"},
		"a high limit":       {"signup: open\n", "custom_id:\n  pattern: a\n  duplicate_limit: 1001\n", "between 1 and 1000"},
		"another word":       {"signup: open\n", "custom_id: owner\n", "`user` or a mapping"},
		"an unknown key":     {"signup: open\n", "custom_id:\n  pattern: a\n  regex: b\n", `unknown key "regex"`},
		"user without users": {"auth: disabled\n", "custom_id: user\n", "auth enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := softTree(t, tc.realm, tc.yaml, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one with %q", err, tc.want)
			}
		})
	}
}

func TestCustomIDCheck(t *testing.T) {
	reg, err := softTree(t, "signup: open\n", "custom_id:\n  pattern: '[a-z]+|.*'\n", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "orders")
	// The pattern is anchored: an alternative that would match a part doesn't let a wider id in,
	// and a pattern that accepts anything still meets the envelope.
	for id, ok := range map[string]bool{
		"abc": true, "INV-2026-0042": true, "a.b": true, "a:b@c~d_e": true,
		"": false, "_files": false, ".hidden": false, "a..b": false, "a/b": false, "a b": false, "é": false,
		strings.Repeat("a", 128): true, strings.Repeat("a", 129): false,
	} {
		if got := c.CustomID.Check(id) == ""; got != ok {
			t.Errorf("Check(%q) accepted=%v, want %v (%s)", id, got, ok, c.CustomID.Check(id))
		}
	}

	reg, err = softTree(t, "signup: open\n", "custom_id:\n  pattern: 'INV-[0-9]{4}'\n", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ = reg.Collection("shop", "main", "orders")
	if c.CustomID.Check("INV-2026") != "" || c.CustomID.Check("xINV-2026") == "" || c.CustomID.Check("INV-20261") == "" {
		t.Error("the pattern is not anchored")
	}
	if got := c.CustomID.Anchored(); got != "^(?:INV-[0-9]{4})$" {
		t.Errorf("Anchored = %q", got)
	}
}
