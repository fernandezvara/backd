package registry

import (
	"maps"
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

func encryptTreeFiles(t *testing.T, schema, yaml string, extra map[string]string) error {
	t.Helper()
	files := map[string]string{
		"clinic/realm.yaml":                    "signup: open\nroles:\n  staff: {admin: true}\n",
		"clinic/main/patients/schema.json":     schema,
		"clinic/main/patients/collection.yaml": yaml,
	}
	maps.Copy(files, extra)
	_, err := Load(writeTree(t, files))
	return err
}

const patientsSchema = `{"type":"object","properties":{
  "tax_id":{"type":"string"},"notes":{"type":"string"},"owner_ref":{"type":"string"},"seen_at":{"type":"string","format":"date-time","x-backd-store":"date"},
  "tags":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}}}},
  "profile":{"type":"object","properties":{"phone":{"type":"string"}}},"score":{"type":"number"}}}`

func TestEncryptedFieldsAreRefusedWhereSomethingNeedsThePlainValue(t *testing.T) {
	enc := func(field string) string { return "fields:\n  " + field + ":\n    encrypt: {key: HEALTH}\n" }
	for name, tc := range map[string]struct {
		yaml, indexes, want string
	}{
		"indexed":      {enc("tax_id"), `[{"fields": ["tax_id"], "unique": true}]`, "tax_id is in an index of indexes.json"},
		"in a text":    {enc("notes"), `[{"text": {"fields": {"notes": 1}}}]`, "notes is in the text index"},
		"in an array":  {enc("tags.label"), ``, "inside an array"},
		"date":         {enc("seen_at"), ``, "date field"},
		"reference":    {enc("owner_ref") + "references:\n  owner: {field: owner_ref, collection: patients}\n", ``, "source of the reference"},
		"nested twice": {"fields:\n  profile:\n    encrypt: {key: HEALTH}\n  profile.phone:\n    encrypt: {key: HEALTH}\n", ``, "already encrypted as a whole"},
		"replace":      {enc("notes") + "on_owner_delete:\n  action: anonymize\n  replace: {notes: removed}\n", ``, "`replace` target"},
		"aggregate":    {enc("score") + "aggregate:\n  metrics: [score]\n", ``, "grouped or measured by aggregate"},
		"read rule":    {enc("tax_id") + "rules:\n  read: user != nil && document.tax_id == 'B1'\n", ``, "document.tax_id is encrypted"},
	} {
		t.Run(name, func(t *testing.T) {
			extra := map[string]string{}
			if tc.indexes != "" {
				extra["clinic/main/patients/indexes.json"] = tc.indexes
			}
			err := encryptTreeFiles(t, patientsSchema, tc.yaml, extra)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEncryptedFieldsStillLetOtherRulesReadThem(t *testing.T) {
	// create, update and delete rules are evaluated in memory on plaintext; a read rule may use
	// every other field.
	yaml := "fields:\n  tax_id:\n    encrypt: {key: TAX}\nrules:\n  read: user != nil && document.notes != nil\n  create: user != nil && data.tax_id != ''\n  update: user != nil && document.tax_id == data.tax_id\n"
	if err := encryptTreeFiles(t, patientsSchema, yaml, nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := encryptTreeFiles2(t, yaml)
	c := reg.Realms["clinic"].Databases["main"].Collections["patients"]
	if !c.IsEncrypted("tax_id") || c.IsEncrypted("notes") || !c.Fields["tax_id"].Encrypted {
		t.Errorf("encrypted flags: %+v", c.Fields["tax_id"])
	}
}

func encryptTreeFiles2(t *testing.T, yaml string) (*Registry, error) {
	t.Helper()
	return Load(writeTree(t, map[string]string{
		"clinic/realm.yaml":                    "signup: open\nroles:\n  staff: {admin: true}\n",
		"clinic/main/patients/schema.json":     patientsSchema,
		"clinic/main/patients/collection.yaml": yaml,
	}))
}

func TestSearchableEncryptedFieldsDeclareTheirBlindIndexes(t *testing.T) {
	reg, err := encryptTreeFiles2(t, "fields:\n  tax_id:\n    encrypt: {key: TAX, search: equality, unique: true}\n  notes:\n    encrypt: {key: HEALTH, search: equality, normalize: [trim, lower]}\n  profile.phone:\n    encrypt: {key: HEALTH}\nsoft_delete: true\n")
	if err != nil {
		t.Fatal(err)
	}
	c := reg.Realms["clinic"].Databases["main"].Collections["patients"]
	if !c.IsSearchable("tax_id") || !c.IsSearchable("notes") || c.IsSearchable("profile.phone") || !c.IsEncrypted("profile.phone") {
		t.Errorf("flags: %+v", c.Fields)
	}
	if f, _ := c.EncryptedAt("notes"); f.Search != SearchEquality || strings.Join(f.Normalize, ",") != "trim,lower" || f.Unique {
		t.Errorf("notes = %+v", f)
	}
	var blind []Index
	for _, ix := range c.Indexes {
		if ix.Blind {
			blind = append(blind, ix)
		}
	}
	if len(blind) != 2 {
		t.Fatalf("blind indexes = %+v", c.Indexes)
	}
	// A unique one is unique like any other: a deleted document's value can be used again.
	var tax Index
	for _, ix := range blind {
		if ix.Keys[0].Field == BlindField("tax_id") {
			tax = ix
		}
	}
	if !tax.Unique || tax.Keys[len(tax.Keys)-1].Field != MetaDeletedAt {
		t.Errorf("tax_id index = %+v", tax)
	}
	if BlindField("profile.phone") != "_bidx.profile~phone" {
		t.Errorf("blind field = %s", BlindField("profile.phone"))
	}
	if _, err := encryptTreeFiles2(t, "fields:\n  x:\n    encrypt: {key: K}\n"); err == nil {
		t.Error("an undeclared field")
	}
}

func TestSearchOptionsOfEncryptedFieldsAreChecked(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, want string }{
		"kind":       {"{key: K, search: fuzzy}", `"fuzzy" is not a kind of search`},
		"normalize":  {"{key: K, normalize: [trim]}", "needs `search: equality`"},
		"unique":     {"{key: K, unique: true}", "needs `search: equality`"},
		"operation":  {"{key: K, search: equality, normalize: [reverse]}", `"reverse" is not a normalization`},
		"underscore": {"{key: K, search: equality, normalize: [upper], unique: maybe}", "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := encryptTreeFiles2(t, "fields:\n  tax_id:\n    encrypt: "+tc.cfg+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRevealRulesAreCompiledAgainstTheCollection(t *testing.T) {
	cfg := func(reveal string) string {
		return "fields:\n  tax_id:\n    encrypt: {key: TAX}\n    reveal: " + reveal + "\n"
	}
	reg, err := encryptTreeFiles2(t, cfg(`user != nil && (hasRole(user, 'staff') || document._meta.owner == user.id)`))
	if err != nil {
		t.Fatal(err)
	}
	c := reg.Realms["clinic"].Databases["main"].Collections["patients"]
	if f, _ := c.EncryptedAt("tax_id"); f.Reveal == nil || !strings.Contains(f.RevealSource, "hasRole") || len(c.Revealing()) != 1 {
		t.Errorf("tax_id = %+v", f)
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown field":   {cfg(`document.nope == 1`), "unknown field document.nope"},
		"data":            {cfg(`data.notes != nil`), "`data` is only available on create and update"},
		"unknown role":    {cfg(`hasRole(user, 'wizard')`), "wizard"},
		"unguarded":       {cfg(`user.id == document._meta.owner`), "user"},
		"syntax":          {cfg(`user !=`), "reveal"},
		"without encrypt": {"fields:\n  notes:\n    reveal: user != nil\n", "declares nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := encryptTreeFiles2(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
