package registry

import (
	"strings"
	"testing"
	"time"
)

const consentRealm = `locales: [en, es]
default_locale: en
consents:
  terms:
    required: true
    current: "2026-10"
    versions:
      "2026-10": { reaccept: true, reaccept_by: 2026-11-15 }
      "2026-03": { reaccept: false }
  marketing:
    required: false
    current: 1
compliance:
  consent_proof: [ip]
  consent_retention: 2y
`

func consentTree(realm string, extra map[string]string) map[string]string {
	files := map[string]string{
		"shop/realm.yaml":                   realm,
		"shop/consents/terms/2026-10/en.md": "New terms",
		"shop/consents/terms/2026-10/es.md": "Nuevos términos",
		"shop/consents/terms/2026-03/en.md": "Old terms",
		"shop/consents/marketing/1/en.md":   "Marketing",
		"shop/consents/marketing/1/es.md":   "Publicidad",
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

func TestConsentsLoad(t *testing.T) {
	reg, err := Load(writeTree(t, consentTree(consentRealm, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s := reg.Realms["shop"].Settings
	terms := s.Consents["terms"]
	if terms == nil || !terms.Required || terms.Current != "2026-10" {
		t.Fatalf("terms = %+v", terms)
	}
	if v := terms.Versions["2026-10"]; !v.Reaccept || !v.ReacceptBy.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("2026-10 = %+v", v)
	}
	if terms.Versions["2026-03"].Reaccept {
		t.Error("a correction asks for re-acceptance")
	}
	txt := terms.Texts["2026-10"]["es"]
	if txt.Body != "Nuevos términos" || len(txt.Hash) != 64 {
		t.Errorf("text = %+v", txt)
	}
	if m := s.Consents["marketing"]; m == nil || m.Required || m.Current != "1" {
		t.Errorf("marketing = %+v", m)
	}
	if !s.Compliance.Proof(ProofIP) || s.Compliance.Proof(ProofUserAgent) || s.Compliance.Retention() != 2*365*24*time.Hour {
		t.Errorf("compliance = %+v", s.Compliance)
	}
	// The consents folder is not a database.
	if len(reg.Realms["shop"].Databases) != 0 {
		t.Errorf("databases: %v", reg.Realms["shop"].Databases)
	}
}

func TestConsentTextsAreInTheFingerprint(t *testing.T) {
	fp := func(extra map[string]string) string {
		reg, err := Load(writeTree(t, consentTree(consentRealm, extra)))
		if err != nil {
			t.Fatal(err)
		}
		f, err := reg.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if fp(nil) == fp(map[string]string{"shop/consents/terms/2026-10/en.md": "New terms, changed"}) {
		t.Error("a changed consent text keeps the fingerprint")
	}
}

func TestConsentsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		realm string
		extra map[string]string
		want  string
	}{
		"missing locale":    {consentRealm, map[string]string{"shop/consents/terms/2026-10/es.md": ""}, ""}, // an empty file still counts
		"no text":           {strings.Replace(consentRealm, "current: 1", "current: 9", 1), nil, "current version \"9\" has no text"},
		"bad date":          {strings.Replace(consentRealm, "2026-11-15", "soon", 1), nil, "reaccept_by"},
		"bad proof":         {strings.Replace(consentRealm, "[ip]", "[cookie]", 1), nil, "consent_proof"},
		"bad retention":     {strings.Replace(consentRealm, "2y", "soon", 1), nil, "consent_retention"},
		"bad name":          {strings.Replace(consentRealm, "  terms:", "  Terms:", 1), nil, "lower-case"},
		"bad version":       {strings.Replace(consentRealm, `current: "2026-10"`, `current: "../x"`, 1), nil, "version"},
		"unlisted old text": {strings.Replace(consentRealm, `      "2026-03": { reaccept: false }`, `      "2025-01": { reaccept: false }`, 1), nil, "has no text"},
		"auth disabled":     {"auth: disabled\n" + consentRealm, nil, "only apply when auth is enabled"},
	} {
		_, err := Load(writeTree(t, consentTree(tc.realm, tc.extra)))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v", name, err)
		}
	}
	// A required current version needs its text in every language.
	files := consentTree(consentRealm, nil)
	delete(files, "shop/consents/terms/2026-10/es.md")
	if _, err := Load(writeTree(t, files)); err == nil || !strings.Contains(err.Error(), "language es") {
		t.Errorf("missing es text: %v", err)
	}
}
