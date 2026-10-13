package registry

import (
	"slices"
	"strings"
	"testing"
)

func TestPersonalData(t *testing.T) {
	load := func(collection string) (*Realm, error) {
		reg, err := Load(writeTree(t, map[string]string{
			"shop/realm.yaml":               "roles:\n  admin: {}\n",
			"shop/db/items/schema.json":     personSchema,
			"shop/db/items/collection.yaml": collection,
			"shop/db/other/schema.json":     personSchema,
		}))
		if err != nil {
			return nil, err
		}
		return reg.Realms["shop"], nil
	}

	// Without a declaration every collection is exported whole.
	rl, err := load("rules:\n  read: 'true'\n")
	if err != nil {
		t.Fatal(err)
	}
	scope := rl.PersonalScope()
	if len(scope) != 2 || scope[0].Collection != "items" || scope[0].Export != ExportInclude || !scope[0].Owned || scope[0].Declared || scope[0].Fields != nil {
		t.Fatalf("default scope = %+v", scope)
	}

	rl, err = load("personal_data:\n  fields: [name]\n  reason: the rest is internal scoring\non_owner_delete:\n  pull: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	e := rl.PersonalScope()[0]
	if !slices.Equal(e.Fields, []string{"name"}) || e.Reason == "" || !e.Declared || e.Export != ExportInclude {
		t.Errorf("partial scope = %+v", e)
	}

	rl, err = load("personal_data:\n  export: exclude\n  reason: fraud scores\n")
	if err != nil {
		t.Fatal(err)
	}
	if e := rl.PersonalScope()[0]; e.Export != ExportExclude || e.Owned || e.Reason != "fraud scores" {
		t.Errorf("excluded scope = %+v", e)
	}

	for name, tc := range map[string]struct{ yaml, want string }{
		"exclude without reason": {"personal_data:\n  export: exclude\n", "reason"},
		"fields without reason":  {"personal_data:\n  fields: [name]\n", "reason"},
		"unknown field":          {"personal_data:\n  fields: [nope]\n  reason: x\n", "not a field"},
		"system field":           {"personal_data:\n  fields: [id]\n  reason: x\n", "system field"},
		"fields with exclude":    {"personal_data:\n  export: exclude\n  fields: [name]\n  reason: x\n", "only apply"},
		"bad export":             {"personal_data:\n  export: maybe\n", "include or exclude"},
		"unknown key":            {"personal_data:\n  scope: all\n", "scope"},
	} {
		if _, err := load(tc.yaml); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "collection.yaml") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
