package registry

import (
	"strings"
	"testing"
)

const layoutSchema = `{"type":"object"}`

// Everything a realm shows and configures lives in _realm; everything else is a database.
func TestRealmFolderIsNotADatabase(t *testing.T) {
	reg, err := Load(writeTree(t, map[string]string{
		"shop/main/orders/schema.json":      layoutSchema,
		"shop/_realm/pages/result/en.html":  "x",
		"shop/_realm/email/welcome/en.html": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	dbs := reg.Realms["shop"].Databases
	if len(dbs) != 1 || dbs["main"] == nil {
		t.Errorf("databases = %v, want only main", dbs)
	}
}

// A database may be named like one of the folders _realm holds: it is told apart by what it has.
func TestDatabasesMayBeNamedLikeRealmFolders(t *testing.T) {
	reg, err := Load(writeTree(t, map[string]string{
		"shop/email/outbox/schema.json": layoutSchema,
		"shop/pages/views/schema.json":  layoutSchema,
		"shop/push/devices/schema.json": layoutSchema,
		"shop/consents/log/schema.json": layoutSchema,
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"email", "pages", "push", "consents"} {
		if reg.Realms["shop"].Databases[name] == nil {
			t.Errorf("database %q was not loaded", name)
		}
	}
}

func TestLayoutErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"another reserved folder in a realm": {
			map[string]string{"shop/_other/x/schema.json": layoutSchema},
			`names starting with "_" are reserved for backd, and a realm has only _realm`,
		},
		"functions at the realm": {
			map[string]string{"shop/_functions/hello/function.yaml": "x"},
			`names starting with "_" are reserved for backd, and a realm has only _realm`,
		},
		"another reserved folder in a database": {
			map[string]string{"shop/main/_realm/email/x.txt": "x"},
			`names starting with "_" are reserved for backd, and a database has only _functions`,
		},
		"an unknown folder in _realm": {
			map[string]string{"shop/_realm/emial/welcome/en.html": "x"},
			`unknown folder "emial": _realm holds email, pages, push, consents, compliance, config`,
		},
		"the old email folder": {
			map[string]string{"shop/email/welcome/en.html": "x"},
			"_realm/email",
		},
		"the old pages folder": {
			map[string]string{"shop/pages/result/en.html": "x"},
			"_realm/pages",
		},
		"the old consents folder": {
			map[string]string{"shop/consents/terms/2026-10/en.md": "x"},
			"_realm/consents",
		},
		"the old compliance folder": {
			map[string]string{"shop/compliance/readme/en.html": "x"},
			"_realm/compliance",
		},
		"the old app configuration folder": {
			map[string]string{"shop/app-config/schema.json": layoutSchema, "shop/app-config/rules.yaml": "read: {}\n"},
			"_realm/config",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTree(t, tc.files))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A file named like a reserved folder is not a folder: it is ignored, as other plain files are.
func TestFilesAtTheRealmLevelAreIgnored(t *testing.T) {
	if _, err := Load(writeTree(t, map[string]string{
		"shop/main/orders/schema.json": layoutSchema,
		"shop/_notes.txt":              "x",
	})); err != nil {
		t.Fatal(err)
	}
}
