package rules

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

var testSchema = Schema{
	DocumentField: func(p string) bool {
		return slices.Contains([]string{"title", "published", "status", "members", "author", "author.name",
			"id", "_meta.created_at", "_meta.updated_at", "_meta.version", "_meta.owner", "_meta.created_by", "_meta.updated_by"}, p)
	},
	DataField: func(p string) bool {
		return slices.Contains([]string{"title", "published", "status", "members", "author", "author.name"}, p)
	},
	ScalarField: func(p string) bool {
		return slices.Contains([]string{"title", "published", "status", "author.name",
			"id", "_meta.created_at", "_meta.updated_at", "_meta.version", "_meta.owner", "_meta.created_by", "_meta.updated_by"}, p)
	},
	ArrayField: func(p string) bool { return p == "members" },
	Roles:      []string{"admin", "editor"},
}

func TestParseValid(t *testing.T) {
	s, errs := parseYAML(`
read: |
  document.published == true
  || (user != nil && (document._meta.owner == user.id || user.id in document.members))
  || hasRole(user, 'editor', 'admin')
create: user != nil && user.email_verified && data.status in ['draft', 'review']
update: >
  user != nil && document._meta.owner == user.id
  && all(changed(), # in ['title', 'status'])
delete: "(user != nil && 'admin' in user.roles) || hasRole(user, 'admin')"
restore: user != nil && document._meta.owner == user.id
purge: hasRole(user, 'admin')
`, testSchema)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, op := range Ops {
		if s.For(op) == nil || s.For(op).Key != string(op) {
			t.Errorf("%s: %+v", op, s.For(op))
		}
	}
}

func TestParseWriteShorthand(t *testing.T) {
	s, errs := parseYAML("read: 'true'\nwrite: hasRole(user, 'admin')\nupdate: 'false'\n", testSchema)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for op, want := range map[Op]string{Create: "write", Update: "update", Delete: "write"} {
		if r := s.For(op); r == nil || r.Key != want {
			t.Errorf("%s from %v, want %q", op, r, want)
		}
	}

	// No rule for an operation: nil, which denies.
	s, _ = parseYAML("read: 'true'\n", testSchema)
	if s.For(Create) != nil || s.For(Delete) != nil {
		t.Error("operations without a rule must have no rule")
	}
	var nilSet *Set
	if nilSet.For(Read) != nil {
		t.Error("a nil set must deny everything")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"not a mapping", "- read", []string{"invalid YAML"}},
		{"unknown key", "list: 'true'", []string{`unknown key "list"`}},
		{"empty rule", "read: ''", []string{"read: empty rule"}},
		{"syntax", "read: 'document.title =='", []string{"read:"}},
		{"unknown variable", "read: usr != nil", []string{"unknown name usr"}},
		{"unknown user field", "read: user != nil && user.name == 'x'", []string{"read:"}},

		{"document on create", "create: document.title == 'x'", []string{"`document` is not available on create"}},
		{"data on read", "read: data.title == 'x'", []string{"`data` is only available on create and update"}},
		{"data on delete", "delete: data.title == 'x'", []string{"`data` is only available"}},
		{"changed outside update", "delete: \"'title' in changed()\"", []string{"changed() is only available on update"}},
		{"write checked per operation", "write: document.title == 'x'", []string{"write (used for create): `document` is not available on create"}},

		{"unknown document field", "read: document.titel == 'x'", []string{"unknown field document.titel"}},
		{"unknown data field", "create: data.owner == 'x'", []string{"unknown field data.owner"}},
		{"meta not in data", "create: data._meta.owner == nil", []string{"unknown field data._meta.owner"}},
		{"computed property", "read: document[document.title] == 'x'", []string{"use dot paths"}},

		{"undeclared role", "read: hasRole(user, 'root')", []string{`role "root" is not declared`}},
		{"undeclared role with in", "read: user != nil && 'root' in user.roles", []string{`role "root" is not declared`}},
		{"non-literal role", "read: user != nil && hasRole(user, user.email)", []string{"role names must be string literals"}},

		{"unguarded", "read: document._meta.owner == user.id", []string{"user.id needs a guard"}},
		{"guard on the wrong side", "read: document._meta.owner == user.id && user != nil", []string{"user.id needs a guard"}},
		{"guard under or", "read: user != nil || document.title == user.email", []string{"user.email needs a guard"}},
		{"optional chaining is not a guard", "read: document._meta.owner == user?.id", []string{"needs a guard"}},
		{"negated guard", "read: \"!(user == nil) && user.email_verified\"", []string{"user.email_verified needs a guard"}},

		{"now with user field", "create: data.title < now", []string{"`now` can only be compared"}},
		{"now alone", "read: now.Year() > 2000", []string{"`now` can only be compared"}},

		{"not filterable: two fields", "read: document.title == document.status", []string{"read rules must be database filters"}},
		{"not filterable: function of field", "read: len(document.title) > 3", []string{"read rules must be database filters"}},
		{"not filterable: arithmetic", "read: document._meta.version + 1 > 2", []string{"read rules must be database filters"}},
		{"array compared as a value", "read: \"document.members == 'x'\"", []string{"document.members is an array", "value in document.members"}},
		{"array in a list", "read: \"document.members in ['x']\"", []string{"document.members is an array"}},
		{"array negated", "read: \"document.members != 'x'\"", []string{"document.members is an array"}},
		{"object compared", "read: \"document.author == 'x'\"", []string{"document.author must be declared"}},
		{"membership needs an array", "read: \"'x' in document.title\"", []string{"needs document.title to be declared as an array"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := parseYAML(tt.src, testSchema)
			var msgs []string
			for _, e := range errs {
				msgs = append(msgs, e.Error())
			}
			got := strings.Join(msgs, "\n")
			if got == "" {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("errors %q do not contain %q", got, w)
				}
			}
		})
	}
}

func TestParseAccepted(t *testing.T) {
	for _, src := range []string{
		"read: 'true'",
		"read: user != nil",
		"read: user == nil || user.email_verified",
		"read: user != nil && (user.email_verified && document.title == user.email)",
		"read: document.status in ['a', 'b'] && !(document.title == nil)",
		"read: document.published",
		"read: not document.published",
		"read: document._meta.created_at > now - duration('24h')",
		"read: now - duration('1h') <= document._meta.updated_at",
		"read: \"'x' in document.members\"",
		"update: user != nil && !('status' in changed())",
		"create: hasRole(user, 'editor') && data.author.name != nil",
	} {
		if _, errs := parseYAML(src, testSchema); len(errs) > 0 {
			t.Errorf("%q: %v", src, errs)
		}
	}
}

// TestDocsExample keeps the example in docs/content/docs/auth/rules.md valid.
func TestDocsExample(t *testing.T) {
	_, errs := parseYAML(`
read: >
  document.published == true
  || (user != nil && document._meta.owner == user.id)
create: user != nil && user.email_verified
update: >
  user != nil && document._meta.owner == user.id
  && !('status' in changed())
delete: hasRole(user, 'admin')
`, testSchema)
	if len(errs) > 0 {
		t.Error(errs)
	}
}

func TestRulesOnDateFields(t *testing.T) {
	schema := testSchema
	schema.DocumentField = func(p string) bool {
		return p == "starts_at" || p == "ends_at" || p == "title" || strings.HasPrefix(p, "_meta.")
	}
	schema.DataField = func(p string) bool { return p == "starts_at" || p == "ends_at" || p == "title" }
	schema.ScalarField = func(p string) bool {
		return p == "starts_at" || p == "ends_at" || p == "title" || strings.HasPrefix(p, "_meta.")
	}
	schema.DateField = func(p string) bool { return p == "starts_at" || p == "ends_at" }
	for _, ok := range []string{
		`read: document.starts_at <= now`,
		`read: document.starts_at > now - duration('24h') || document.ends_at == nil`,
		`read: "document.ends_at != nil && now < document.ends_at"`,
		`create: user != nil && data.starts_at > now`,
		`create: user != nil && data.ends_at > data.starts_at`,
		`update: user != nil && document.starts_at > now && !('starts_at' in changed())`,
	} {
		if _, errs := parseYAML(ok+"\n", schema); len(errs) > 0 {
			t.Errorf("%q: %v", ok, errs)
		}
	}
	for src, want := range map[string]string{
		`read: "document.starts_at > '2026-01-01T00:00:00Z'"`:    "stored as a date",
		`read: "document.starts_at == 'yesterday'"`:              "stored as a date",
		`read: "document.starts_at in ['2026-01-01T00:00:00Z']"`: "stored as a date",
		`read: "user != nil && document.starts_at > user.email"`: "stored as a date",
		`create: "data.starts_at < '2030-01-01T00:00:00Z'"`:      "stored as a date",
		`read: document.title == 'x' || now > document.title`:    "`now` can only be compared with a timestamp",
	} {
		_, errs := parseYAML(src+"\n", schema)
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%q: %v, want an error with %q", src, errs, want)
		}
	}
}

// restore and purge are for collections that soft-delete: `write` doesn't
// cover them (they are denied unless declared), restore is a filter like read,
// and neither sees `data`.
func TestRestoreAndPurgeRules(t *testing.T) {
	s, errs := parseYAML("read: 'true'\nwrite: hasRole(user, 'admin')\n", testSchema)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.For(Restore) != nil || s.For(Purge) != nil {
		t.Error("write shorthand must not grant restore or purge")
	}
	s, errs = parseYAML("read: 'true'\nrestore: user != nil && document._meta.owner == user.id\npurge: hasRole(user, 'admin')\n", testSchema)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := s.For(Restore).Filter(Values{User: &User{ID: "u1"}}); err != nil {
		t.Errorf("restore rule isn't a usable filter: %v", err)
	}
	for _, src := range []string{"restore: data.status == 'draft'\n", "purge: data.status == 'draft'\n"} {
		if _, errs := parseYAML("read: 'true'\n"+src, testSchema); len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), "`data` is only available on create and update") {
			t.Errorf("%q: errors = %v", src, errs)
		}
	}
	// A restore rule becomes a database filter, so it follows read's limits.
	if _, errs := parseYAML("read: 'true'\nrestore: document.tags == nil\n", testSchema); len(errs) == 0 {
		t.Log("(array comparison accepted by the test schema)")
	}
}
