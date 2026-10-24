package registry

import (
	"errors"
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
		"shop/realm.yaml":                          realm,
		"shop/_realm/consents/terms/2026-10/en.md": "New terms",
		"shop/_realm/consents/terms/2026-10/es.md": "Nuevos términos",
		"shop/_realm/consents/terms/2026-03/en.md": "Old terms",
		"shop/_realm/consents/marketing/1/en.md":   "Marketing",
		"shop/_realm/consents/marketing/1/es.md":   "Publicidad",
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
	if fp(nil) == fp(map[string]string{"shop/_realm/consents/terms/2026-10/en.md": "New terms, changed"}) {
		t.Error("a changed consent text keeps the fingerprint")
	}
}

func TestConsentsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		realm string
		extra map[string]string
		want  string
	}{
		"missing locale":    {consentRealm, map[string]string{"shop/_realm/consents/terms/2026-10/es.md": ""}, ""}, // an empty file still counts
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
	delete(files, "shop/_realm/consents/terms/2026-10/es.md")
	if _, err := Load(writeTree(t, files)); err == nil || !strings.Contains(err.Error(), "language es") {
		t.Errorf("missing es text: %v", err)
	}
}

func TestConsentsToAccept(t *testing.T) {
	reg, err := Load(writeTree(t, consentTree(consentRealm, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s := reg.Realms["shop"].Settings
	for name, tc := range map[string]struct {
		held map[string]string
		want []ConsentRef
	}{
		"nothing held":        {nil, []ConsentRef{{"terms", "2026-10"}}},
		"current":             {map[string]string{"terms": "2026-10"}, nil},
		"older, reaccept":     {map[string]string{"terms": "2026-03"}, []ConsentRef{{"terms", "2026-10"}}},
		"optional is ignored": {map[string]string{"terms": "2026-10", "marketing": ""}, nil},
	} {
		got := s.ConsentsToAccept(tc.held)
		if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	// reaccept_by 2026-11-15 covers that whole day (UTC): the deadline is the day after.
	held := map[string]string{"terms": "2026-03"}
	for when, want := range map[time.Time]int{
		time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC): 0,
		time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC):  1,
	} {
		if got := len(s.ConsentsPastDeadline(held, when)); got != want {
			t.Errorf("past deadline at %s = %d, want %d", when, got, want)
		}
	}
	// Someone who never accepted is gated once the current version's date has passed too.
	if got := s.ConsentsPastDeadline(nil, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); len(got) != 1 {
		t.Errorf("a missing consent past the version's date = %v", got)
	}
}

const exportEmail = "email:\n  function: app/deliver\n  from: \"Acme <no-reply@acme.example>\"\n  public_url: https://api.acme.example\n  allowed_redirects: [https://app.acme.example]\n"

func TestComplianceExportSettings(t *testing.T) {
	ok := storageBase + exportEmail + "compliance:\n  export: true\n  export_page: https://app.acme.example/my-data\n"
	s, errs := parseRealmSettings([]byte(ok))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	c := s.Compliance
	if !c.Export || c.ExportPage != "https://app.acme.example/my-data" || c.ExportKeep() != DefaultExportRetention {
		t.Errorf("settings = %+v", c)
	}
	s, errs = parseRealmSettings([]byte(ok + "  export_retention: 14d\n"))
	if len(errs) != 0 || s.Compliance.ExportKeep() != 14*24*time.Hour {
		t.Errorf("retention = %v %v", s.Compliance.ExportKeep(), errs)
	}

	for name, tc := range map[string]struct{ yaml, want string }{
		"no storage":        {exportEmail + "compliance:\n  export: true\n  export_page: https://app.acme.example/x\n", "`storage:` must be configured"},
		"no email":          {storageBase + "compliance:\n  export: true\n  export_page: https://app.acme.example/x\n", "`email:` must be configured"},
		"no page":           {storageBase + exportEmail + "compliance:\n  export: true\n", "export_page: required"},
		"page elsewhere":    {storageBase + exportEmail + "compliance:\n  export: true\n  export_page: https://evil.example/x\n", "within sign_in.allowed_redirects"},
		"bad retention":     {ok + "  export_retention: soon\n", "export_retention"},
		"zero retention":    {ok + "  export_retention: 0\n", "longer than zero"},
		"page without it":   {"compliance:\n  export_page: https://app.acme.example/x\n", "no effect while compliance.export"},
		"on, auth disabled": {"auth: disabled\n" + ok, "only apply when auth is enabled"},
	} {
		_, errs := parseRealmSettings([]byte(tc.yaml))
		if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), tc.want) {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// Off by default, and leaving it off asks for nothing.
	if s, errs := parseRealmSettings([]byte("compliance:\n  consent_retention: 1y\n")); len(errs) != 0 || s.Compliance.Export {
		t.Errorf("default: %+v %v", s.Compliance, errs)
	}
}

func TestExportReadmeTemplates(t *testing.T) {
	realm := "locales: [en, es]\ndefault_locale: en\n" + goodEmail + "  allowed_redirects: [https://app.shop.example]\n" + strings.Replace(storageBase, "\n", "\n", 1) +
		"compliance:\n  export: true\n  export_page: https://app.shop.example/my-data\n"
	tree := func(extra map[string]string) string {
		files := esTemplates(t)
		for k, v := range extra {
			files[k] = v
		}
		return emailTree(t, realm, goodDeliver, files)
	}

	// None of its own: the built-in ones are used.
	reg, err := Load(tree(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Realms["shop"].ExportReadme("en"); ok {
		t.Error("a README that nobody wrote")
	}

	// Its own, for a language the realm lists; the other falls back.
	reg, err = Load(tree(map[string]string{"shop/_realm/compliance/readme/es.html": "<p>Tus datos en {{Realm}}</p>"}))
	if err != nil {
		t.Fatal(err)
	}
	if src, ok := reg.Realms["shop"].ExportReadme("es"); !ok || !strings.Contains(src, "Tus datos") {
		t.Errorf("es = %q %v", src, ok)
	}
	if _, ok := reg.Realms["shop"].ExportReadme("en"); ok {
		t.Error("English has no template of its own")
	}
	fpWith, _ := reg.Fingerprint()
	reg2, err := Load(tree(map[string]string{"shop/_realm/compliance/readme/es.html": "<p>Otros datos en {{Realm}}</p>"}))
	if err != nil {
		t.Fatal(err)
	}
	if fp2, _ := reg2.Fingerprint(); fpWith == "" || fpWith == fp2 {
		t.Error("changing the README keeps the fingerprint")
	}

	for name, tc := range map[string]struct{ file, content, want string }{
		"bad syntax":       {"shop/_realm/compliance/readme/en.html", "<p>{{#Exclusions}}", "line"},
		"unknown language": {"shop/_realm/compliance/readme/fr.html", "<p>x</p>", "not a language of this realm"},
		"not html":         {"shop/_realm/compliance/readme/en.txt", "x", "<language>.html"},
	} {
		_, err := Load(tree(map[string]string{tc.file: tc.content}))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
