package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

func TestWhoIsShownEncryptedFieldsInPlain(t *testing.T) {
	rule, err := rules.CompileReveal("user != nil && document._meta.owner == user.id", rules.Schema{
		DocumentField: func(p string) bool { return p == "_meta.owner" || p == "notes" },
		DataField:     func(string) bool { return false }, ScalarField: func(string) bool { return true }, ArrayField: func(string) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{
		{Path: "notes", Key: "K", Reveal: rule}, {Path: "tax_id", Key: "K"},
	}}
	d := &documents{now: time.Now}
	doc := map[string]any{"id": "p1", "notes": "n", "tax_id": "t", "_meta": map[string]any{"owner": "u1"}}
	as := func(caller auth.Caller) *revealer {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
		return d.revealerFor(r, c)
	}
	user := func(id string) auth.Caller {
		return auth.Caller{User: &auth.Principal{User: auth.User{ID: id}}}
	}
	for name, tc := range map[string]struct {
		caller auth.Caller
		hidden string
	}{
		"the owner":              {user("u1"), ""},
		"another user":           {user("u2"), "notes"},
		"a data API key":         {auth.Caller{Key: &auth.APIKey{Name: "svc"}}, ""},
		"a function, as itself":  {auth.Caller{Func: &auth.FuncCaller{Admin: true}}, ""},
		"a function, for a user": {auth.Caller{User: user("u2").User, Func: &auth.FuncCaller{}}, "notes"},
	} {
		t.Run(name, func(t *testing.T) {
			shown, hidden := as(tc.caller).view(doc)
			got := ""
			if len(hidden) > 0 {
				got = hidden[0]
			}
			if got != tc.hidden {
				t.Errorf("hidden = %v, want %q", hidden, tc.hidden)
			}
			if tc.hidden == "" && shown["notes"] != "n" || tc.hidden != "" && shown["notes"] != nil || shown["tax_id"] != "t" {
				t.Errorf("shown = %v", shown)
			}
			if doc["notes"] != "n" {
				t.Error("the document was modified")
			}
		})
	}
	var none *revealer
	if shown, hidden := none.view(doc); shown["notes"] != "n" || hidden != nil {
		t.Error("a nil revealer must show everything")
	}
}

func TestKeptAnswersHoldNoEncryptedField(t *testing.T) {
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{{Path: "tax_id", Key: "K"}}}
	d := &documents{now: time.Now}
	doc := map[string]any{"id": "p1", "tax_id": "t", "name": "Ada", "_meta": map[string]any{"version": 1}}
	kept, hidden := d.storedView(c, doc)
	if kept["tax_id"] != nil || kept["name"] != "Ada" || len(hidden) != 1 {
		t.Errorf("kept = %v, hidden = %v", kept, hidden)
	}
	if doc["tax_id"] != "t" {
		t.Error("the document was modified")
	}
	plain := &registry.Collection{Name: "notes"}
	if same, _ := d.storedView(plain, doc); same["tax_id"] != "t" {
		t.Error("a collection without encrypted fields keeps its answers whole")
	}
}
