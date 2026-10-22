package registry

import (
	"strings"
	"testing"
)

func anonRealm(t *testing.T, policy string) *Realm {
	t.Helper()
	reg, err := erasureTree(t, "signup: open\n", policy)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Realms["shop"]
}

func TestAnonymizeRulesFromThePolicy(t *testing.T) {
	rl := anonRealm(t, "on_owner_delete:\n  action: anonymize\n  remove: [phone, address.street]\n  replace: {owner_ref: \"x\", buyer_name: \"Anon\"}\n  pull: {members: email}\n  unset: {paid_by: email}\n")
	rules, err := AnonymizeRules(rl, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rules["main/orders"]
	if r == nil || strings.Join(r.Remove, ",") != "address.street,phone" || r.Replace["buyer_name"] != "Anon" || strings.Join(r.Emails, ",") != "members,paid_by" {
		t.Errorf("rule: %+v", r)
	}
}

func TestAnonymizeRulesWithoutAPolicy(t *testing.T) {
	rl := anonRealm(t, "")
	rules, err := AnonymizeRules(rl, nil)
	if err != nil || len(rules) != 0 {
		t.Fatalf("%v %v", rules, err)
	}
	// An override is a rule of its own.
	o, err := ParseAnonymizeConfig([]byte("collections:\n  main/orders:\n    remove: [phone]\n    replace: {buyer_name: Anonymous}\n"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err = AnonymizeRules(rl, o)
	if err != nil || rules["main/orders"] == nil || rules["main/orders"].Replace["buyer_name"] != "Anonymous" {
		t.Fatalf("%v %v", rules, err)
	}
}

func TestAnonymizeOverrideWinsOverThePolicy(t *testing.T) {
	rl := anonRealm(t, "on_owner_delete:\n  action: anonymize\n  remove: [phone]\n  replace: {buyer_name: \"Erased\"}\n")
	o, _ := ParseAnonymizeConfig([]byte("collections:\n  main/orders:\n    remove: [buyer_name, total]\n    replace: {phone: \"000\"}\n"))
	rules, err := AnonymizeRules(rl, o)
	if err != nil {
		// buyer_name is required: removing it makes documents invalid.
		if !strings.Contains(err.Error(), "buyer_name is required by schema.json") {
			t.Fatal(err)
		}
	} else {
		t.Fatalf("a required field was removed: %v", rules)
	}
	o, _ = ParseAnonymizeConfig([]byte("collections:\n  main/orders:\n    remove: [total]\n    replace: {phone: \"000\"}\n"))
	rules, err = AnonymizeRules(rl, o)
	if err != nil {
		t.Fatal(err)
	}
	r := rules["main/orders"]
	if strings.Join(r.Remove, ",") != "total" || r.Replace["phone"] != "000" || r.Replace["buyer_name"] != "Erased" {
		t.Errorf("merged: %+v", r)
	}
}

func TestAnonymizeConfigErrors(t *testing.T) {
	rl := anonRealm(t, "")
	for name, tc := range map[string]struct{ cfg, want string }{
		"a collection that isn't there": {"collections:\n  main/ghost:\n    remove: [phone]\n", "main/ghost is not a collection"},
		"a field that isn't there":      {"collections:\n  main/orders:\n    remove: [nothing]\n", "nothing is not a field of schema.json"},
		"a system field":                {"collections:\n  main/orders:\n    remove: [_meta.owner]\n", "system field"},
		"a required field":              {"collections:\n  main/orders:\n    remove: [owner_ref]\n", "owner_ref is required"},
		"a value the schema refuses":    {"collections:\n  main/orders:\n    replace: {total: \"a lot\"}\n", "doesn't satisfy schema.json"},
		"a unique field":                {"collections:\n  main/orders:\n    replace: {code: \"X\"}\n", "unique index"},
	} {
		o, err := ParseAnonymizeConfig([]byte(tc.cfg))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		_, err = AnonymizeRules(rl, o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseAnonymizeConfig([]byte("colections: {}")); err == nil {
		t.Error("a misspelt key was accepted")
	}
}
