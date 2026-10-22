package registry

import (
	"strings"
	"testing"
)

func encryptTree(t *testing.T, yaml string) (*Registry, error) {
	t.Helper()
	return Load(writeTree(t, map[string]string{
		"clinic/realm.yaml":                    "signup: open\n",
		"clinic/main/patients/schema.json":     `{"type":"object","properties":{"tax_id":{"type":"string"},"notes":{"type":"string"},"address":{"type":"object","properties":{"street":{"type":"string"}}}}}`,
		"clinic/main/patients/collection.yaml": yaml,
	}))
}

func TestEncryptedFieldsAreDeclaredAndListedByKey(t *testing.T) {
	reg, err := encryptTree(t, "fields:\n  tax_id:\n    encrypt: {key: TAX}\n  notes:\n    encrypt: {key: HEALTH}\n  address.street:\n    encrypt: {key: HEALTH}\n")
	if err != nil {
		t.Fatal(err)
	}
	rl := reg.Realms["clinic"]
	c := rl.Databases["main"].Collections["patients"]
	if len(c.Encrypted) != 3 || c.Encrypted[0].Path != "address.street" || c.Encrypted[2].Key != "TAX" {
		t.Errorf("encrypted = %+v", c.Encrypted)
	}
	if got := strings.Join(rl.EncryptionKeyUses("HEALTH"), ","); got != "main/patients.address.street,main/patients.notes" {
		t.Errorf("uses = %s", got)
	}
	if got := strings.Join(rl.EncryptionKeyNames(), ","); got != "HEALTH,TAX" {
		t.Errorf("names = %s", got)
	}
}

func TestEncryptedFieldMistakesAreRefusedWithTheField(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"undeclared": {"fields:\n  ssn:\n    encrypt: {key: TAX}\n", `"ssn" is not declared in schema.json`},
		"no key":     {"fields:\n  tax_id:\n    encrypt: {}\n", "tax_id.encrypt.key"},
		"bad key":    {"fields:\n  tax_id:\n    encrypt: {key: tax}\n", "tax_id.encrypt.key"},
		"nothing":    {"fields:\n  tax_id: {}\n", "tax_id: declares nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := encryptTree(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
