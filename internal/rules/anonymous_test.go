package rules

import "testing"

func TestCanAllowAnonymous(t *testing.T) {
	schema := testSchema
	for _, tc := range []struct {
		src     string
		anon    bool
		usesNow bool
	}{
		{`true`, true, false},
		{`user == nil`, true, false},
		{`user != nil`, false, false},
		{`user != nil && user.id == document._meta.owner`, false, false},
		{`document.status == 'published'`, true, false},
		{`document.status == 'published' || user != nil`, true, false},
		{`user != nil && document.status == 'published'`, false, false},
		{`hasRole(user, 'admin')`, false, false},
		{`hasRole(user, 'admin') || document.status == 'published'`, true, false},
		{`!(user == nil)`, false, false},
		{`not (user != nil)`, true, false},
		{`document._meta.created_at < now - duration('1h')`, true, true},
	} {
		set, errs := Parse(map[string]string{"read": tc.src}, schema)
		if len(errs) > 0 {
			t.Fatalf("%s: %v", tc.src, errs)
		}
		r := set.For(Read)
		if got := r.CanAllowAnonymous(); got != tc.anon {
			t.Errorf("CanAllowAnonymous(%s) = %v", tc.src, got)
		}
		if got := r.UsesNow(); got != tc.usesNow {
			t.Errorf("UsesNow(%s) = %v", tc.src, got)
		}
	}
}
