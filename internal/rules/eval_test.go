package rules

import (
	"reflect"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

func mustRule(t *testing.T, src string, op Op) *Rule {
	t.Helper()
	s, errs := Parse([]byte(src), testSchema)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return s.For(op)
}

func TestFilter(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ada := &User{ID: "u1", Email: "ada@example.com", Roles: []string{"editor"}}
	bob := &User{ID: "u2"}
	cond := func(f string, op storage.Operator, v any) storage.Condition {
		return storage.Condition{Field: f, Op: op, Value: v}
	}
	tests := []struct {
		name string
		rule string
		user *User
		want storage.Filter
	}{
		{"public", `read: "true"`, nil, storage.Const(true)},
		{"signed in only: anonymous", `read: user != nil`, nil, storage.Const(false)},
		{"signed in only: user", `read: user != nil`, bob, storage.Const(true)},
		{"owner: anonymous", `read: user != nil && document._meta.owner == user.id`, nil, storage.Const(false)},
		{"owner: user", `read: user != nil && document._meta.owner == user.id`, ada, cond("_meta.owner", storage.OpEq, "u1")},
		{"published or owner: anonymous", `read: document.published || (user != nil && document._meta.owner == user.id)`, nil,
			cond("published", storage.OpEq, true)},
		{"published or owner: user", `read: document.published || (user != nil && document._meta.owner == user.id)`, ada,
			storage.Or{cond("published", storage.OpEq, true), cond("_meta.owner", storage.OpEq, "u1")}},
		{"role bypass", `read: hasRole(user, 'editor') || document.published == true`, ada, storage.Const(true)},
		{"role bypass: not in role", `read: hasRole(user, 'editor') || document.published == true`, bob, cond("published", storage.OpEq, true)},
		{"members", `read: "user != nil && user.id in document.members"`, bob, cond("members", storage.OpEq, "u2")},
		{"field in list", `read: "document.status in ['a', 'b']"`, nil, cond("status", storage.OpIn, []any{"a", "b"})},
		{"field in user list", `read: "user != nil && document.status in user.roles"`, ada, cond("status", storage.OpIn, []any{"editor"})},
		{"value on the left flips", `read: "3 < document._meta.version"`, nil, cond("_meta.version", storage.OpGt, int64(3))},
		{"nil equality", `read: document.status == nil`, nil, cond("status", storage.OpIsNull, true)},
		{"nil inequality", `read: document.status != nil`, nil, cond("status", storage.OpIsNull, false)},
		{"negation", `read: "!(document.status == 'draft')"`, nil, storage.Not{Filter: cond("status", storage.OpEq, "draft")}},
		{"now arithmetic", `read: document._meta.created_at > now - duration('24h')`, nil,
			cond("_meta.created_at", storage.OpGt, now.Add(-24*time.Hour))},
		{"id", `read: document.id != 'x'`, nil, cond("id", storage.OpNe, "x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustRule(t, tt.rule, Read).Filter(Values{User: tt.user, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestAllow(t *testing.T) {
	ada := &User{ID: "u1", EmailVerified: true, Roles: []string{"admin"}}
	doc := map[string]any{"id": "d1", "title": "a", "status": "draft", "_meta": map[string]any{"owner": "u1", "version": int64(1)}}
	tests := []struct {
		name string
		rule string
		op   Op
		v    Values
		want bool
	}{
		{"create verified", "create: user != nil && user.email_verified", Create, Values{User: ada, Data: map[string]any{"title": "x"}}, true},
		{"create anonymous", "create: user != nil && user.email_verified", Create, Values{Data: map[string]any{"title": "x"}}, false},
		{"create data check", "create: \"data.status in ['draft']\"", Create, Values{Data: map[string]any{"status": "published"}}, false},
		{"update owner", "update: user != nil && document._meta.owner == user.id", Update, Values{User: ada, Document: doc, Data: map[string]any{"title": "b"}}, true},
		{"update not owner", "update: user != nil && document._meta.owner == user.id", Update, Values{User: &User{ID: "u2"}, Document: doc, Data: map[string]any{}}, false},
		{"changed allowed", "update: \"all(changed(), # in ['title'])\"", Update,
			Values{Document: doc, Data: map[string]any{"title": "b", "status": "draft"}}, true},
		{"changed forbidden", "update: \"all(changed(), # in ['title'])\"", Update,
			Values{Document: doc, Data: map[string]any{"title": "b", "status": "published"}}, false},
		{"removed field counts as changed", "update: \"!('status' in changed())\"", Update,
			Values{Document: doc, Data: map[string]any{"title": "a"}}, false},
		{"delete role", "delete: hasRole(user, 'admin')", Delete, Values{User: ada, Document: doc}, true},
		{"delete anonymous", "delete: hasRole(user, 'admin')", Delete, Values{Document: doc}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustRule(t, tt.rule, tt.op).Allow(tt.v)
			if err != nil || got != tt.want {
				t.Errorf("Allow = %v, %v; want %v", got, err, tt.want)
			}
		})
	}

	// A type error at runtime is reported, so the caller can deny.
	r := mustRule(t, "create: data.title > 3", Create)
	if ok, err := r.Allow(Values{Data: map[string]any{"title": "x"}}); ok || err == nil {
		t.Errorf("type mismatch: %v, %v", ok, err)
	}
}

func TestChanged(t *testing.T) {
	before := map[string]any{"id": "1", "_meta": map[string]any{}, "a": int64(1), "b": map[string]any{"x": "y"}, "c": "gone"}
	after := map[string]any{"a": int64(1), "b": map[string]any{"x": "z"}, "d": "new"}
	if got := changed(before, after); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Errorf("changed = %v", got)
	}
}
