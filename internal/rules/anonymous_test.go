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

func TestHasConsent(t *testing.T) {
	read := func(src string, u *User) (bool, error) {
		set, errs := Parse(map[string]string{"read": src}, testSchema)
		if len(errs) > 0 {
			t.Fatalf("%s: %v", src, errs)
		}
		return set.For(Read).Allow(Values{User: u, Document: map[string]any{"published": true}})
	}
	opted := &User{ID: "u1", Consents: map[string]string{"marketing": "1", "terms": "2"}}
	plain := &User{ID: "u2"}
	for src, tc := range map[string]struct {
		user *User
		want bool
	}{
		"hasConsent(user, 'marketing')":                       {opted, true},
		"hasConsent(user, 'marketing', 'terms')":              {opted, true},
		"hasConsent(user, 'marketing')\n":                     {plain, false},
		"hasConsent(user, 'marketing')  ":                     {nil, false},
		"user != nil && user.consents.terms == '2'":           {opted, true},
		"user != nil && user.consents.terms == '2' ":          {plain, false},
		"hasConsent(user, 'marketing') || document.published": {nil, true},
	} {
		got, err := read(src, tc.user)
		if err != nil || got != tc.want {
			t.Errorf("%q = %v %v, want %v", src, got, err, tc.want)
		}
	}
	// A consent the realm doesn't declare, and a name that isn't a literal, are refused.
	for _, src := range []string{"hasConsent(user, 'cookies')", "hasConsent(user, document.status)"} {
		if _, errs := Parse(map[string]string{"read": src}, testSchema); len(errs) == 0 {
			t.Errorf("%q accepted", src)
		}
	}
	// Anonymous callers can't satisfy it, which a public cache needs to know.
	set, _ := Parse(map[string]string{"read": "hasConsent(user, 'marketing')"}, testSchema)
	if set.For(Read).CanAllowAnonymous() {
		t.Error("hasConsent can allow anonymous callers")
	}
}
