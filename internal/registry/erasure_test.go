package registry

import (
	"strings"
	"testing"
)

const ordersSchema = `{
  "type": "object",
  "properties": {
    "buyer_name": {"type": "string", "minLength": 3},
    "phone":      {"type": "string"},
    "address":    {"type": "object", "properties": {"city": {"type": "string"}, "street": {"type": "string"}}, "required": ["city"]},
    "members":    {"type": "array", "items": {"type": "string"}},
    "pinned":     {"type": "array", "items": {"type": "string"}, "minItems": 1},
    "numbers":    {"type": "array", "items": {"type": "integer"}},
    "paid_by":    {"type": "string"},
    "owner_ref":  {"type": "string"},
    "total":      {"type": "integer"},
    "code":       {"type": "string"}
  },
  "required": ["buyer_name", "owner_ref"],
  "additionalProperties": false
}`

func erasureTree(t *testing.T, realm, policy string) (*Registry, error) {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":                  realm,
		"shop/main/orders/schema.json":     ordersSchema,
		"shop/main/orders/indexes.json":    `[{"fields": ["code"], "unique": true}]`,
		"shop/main/orders/collection.yaml": policy,
	}
	return Load(writeTree(t, files))
}

func TestErasurePolicy(t *testing.T) {
	reg, err := erasureTree(t, "signup: open\n", `
on_owner_delete:
  action: anonymize
  remove: [phone, address.street]
  replace:
    buyer_name: "Erased customer"
    total: 0
  pull:
    members: email
  unset:
    paid_by: id
`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "orders")
	p := c.Erasure
	if p == nil || p.Action != ErasureAnonymize || len(p.Remove) != 2 || p.Replace["buyer_name"] != "Erased customer" || p.Pull["members"] != MatchEmail || p.Unset["paid_by"] != MatchID {
		t.Fatalf("policy: %+v", p)
	}
	if got := strings.Join(p.IndexedFields(), ","); got != "members,paid_by" {
		t.Errorf("indexed fields: %s", got)
	}
	// Only references, no action: the owned documents are left alone.
	reg, err = erasureTree(t, "signup: open\n", "on_owner_delete:\n  pull: {members: id}\n")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ = reg.Collection("shop", "main", "orders"); c.Erasure == nil || c.Erasure.Action != "" {
		t.Errorf("a policy with only pull: %+v", c.Erasure)
	}
}

// Without the file, or with an empty one, nothing happens to the collection.
func TestNoErasurePolicy(t *testing.T) {
	for name, policy := range map[string]string{"empty": "", "comments only": "# nothing here\n"} {
		reg, err := erasureTree(t, "signup: open\n", policy)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c, _ := reg.Collection("shop", "main", "orders"); c.Erasure != nil {
			t.Errorf("%s: %+v", name, c.Erasure)
		}
	}
	reg, err := Load(writeTree(t, map[string]string{"shop/realm.yaml": "signup: open\n", "shop/main/orders/schema.json": ordersSchema}))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := reg.Collection("shop", "main", "orders"); c.Erasure != nil {
		t.Errorf("no file: %+v", c.Erasure)
	}
}

func TestErasurePolicyErrors(t *testing.T) {
	for name, tt := range map[string]struct{ policy, want string }{
		"unknown key":              {"on_owner_delete:\n  action: delete\n  keep: [x]\n", "field keep not found"},
		"unknown top key":          {"on_something:\n  action: delete\n", "field on_something not found"},
		"bad action":               {"on_owner_delete:\n  action: keep\n", `action: must be delete or anonymize, got "keep"`},
		"remove without anonymize": {"on_owner_delete:\n  action: delete\n  remove: [phone]\n", "only apply with `action: anonymize`"},
		"unknown field":            {"on_owner_delete:\n  action: anonymize\n  remove: [nope]\n", "nope is not a field"},
		"system field":             {"on_owner_delete:\n  action: anonymize\n  replace: {id: x}\n", "id is a system field"},
		"remove required":          {"on_owner_delete:\n  action: anonymize\n  remove: [buyer_name]\n", "required by schema.json"},
		"remove nested required":   {"on_owner_delete:\n  action: anonymize\n  remove: [address.city]\n", "required by schema.json"},
		"remove and replace":       {"on_owner_delete:\n  action: anonymize\n  remove: [phone]\n  replace: {phone: x}\n", "also in replace"},
		"replace invalid":          {"on_owner_delete:\n  action: anonymize\n  replace: {buyer_name: \"x\"}\n", "doesn't satisfy schema.json"},
		"replace wrong type":       {"on_owner_delete:\n  action: anonymize\n  replace: {total: \"zero\"}\n", "doesn't satisfy schema.json"},
		"replace unique":           {"on_owner_delete:\n  action: anonymize\n  replace: {code: \"erased\"}\n", "unique index"},
		"pull not an array":        {"on_owner_delete:\n  pull: {paid_by: email}\n", "must be an array of strings"},
		"pull wrong items":         {"on_owner_delete:\n  pull: {numbers: id}\n", "must be an array of strings"},
		"pull minItems":            {"on_owner_delete:\n  pull: {pinned: id}\n", "has minItems"},
		"pull bad match":           {"on_owner_delete:\n  pull: {members: name}\n", "email or id"},
		"unset not a string":       {"on_owner_delete:\n  unset: {total: id}\n", "must be a string"},
		"unset required":           {"on_owner_delete:\n  unset: {owner_ref: id}\n", "make it optional"},
		"unset bad match":          {"on_owner_delete:\n  unset: {paid_by: \"\"}\n", "email or id"},
		"unset and pull":           {"on_owner_delete:\n  pull: {members: id}\n  unset: {members: id}\n", "must be a string"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := erasureTree(t, "signup: open\n", tt.policy)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want %q", err, tt.want)
			}
		})
	}
	// A realm without users has nothing to erase.
	if _, err := erasureTree(t, "auth: disabled\n", "on_owner_delete:\n  action: delete\n"); err == nil || !strings.Contains(err.Error(), "auth has") && !strings.Contains(err.Error(), "only applies when the realm has auth enabled") {
		t.Errorf("auth disabled: %v", err)
	}
}

func TestErasurePolicyIsInTheFingerprint(t *testing.T) {
	a, err := erasureTree(t, "signup: open\n", "on_owner_delete:\n  action: delete\n")
	if err != nil {
		t.Fatal(err)
	}
	b, err := erasureTree(t, "signup: open\n", "on_owner_delete:\n  action: anonymize\n")
	if err != nil {
		t.Fatal(err)
	}
	fa, errA := a.Fingerprint()
	fb, errB := b.Fingerprint()
	if errA != nil || errB != nil || fa == fb {
		t.Errorf("changing collection.yaml must change the config fingerprint: %q %q %v %v", fa, fb, errA, errB)
	}
}
