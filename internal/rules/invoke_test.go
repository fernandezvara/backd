package rules

import (
	"strings"
	"testing"
)

func TestCompileInvoke(t *testing.T) {
	roles := []string{"staff"}
	for _, tt := range []struct {
		src  string
		user *User
		want bool
	}{
		{"true", nil, true},
		{"user != nil", nil, false},
		{"user != nil", &User{ID: "u1"}, true},
		{"user != nil && user.email_verified", &User{ID: "u1"}, false},
		{"hasRole(user, 'staff')", &User{ID: "u1", Roles: []string{"staff"}}, true},
		{"hasRole(user, 'staff')", nil, false},
		{"user != nil && 'staff' in user.roles", &User{ID: "u1"}, false},
	} {
		r, err := CompileInvoke(tt.src, roles)
		if err != nil {
			t.Errorf("%s: %v", tt.src, err)
			continue
		}
		if ok, err := r.Allow(Values{User: tt.user}); ok != tt.want || err != nil {
			t.Errorf("%s with %+v = %v, %v; want %v", tt.src, tt.user, ok, err, tt.want)
		}
	}
	for src, want := range map[string]string{
		"":                          "empty rule",
		"document.owner == user.id": "`document` is not available in invoke rules",
		"data.x == 1":               "`data` is not available in invoke rules",
		"'a' in changed()":          "`changed` is not available in invoke rules",
		"user.id == 'x'":            "needs a guard for anonymous callers",
		"hasRole(user, 'admin')":    `role "admin" is not declared in realm.yaml`,
		"now > now":                 "`now` can only be compared",
		"1 + 1":                     "expected bool",
	} {
		if _, err := CompileInvoke(src, roles); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
}
