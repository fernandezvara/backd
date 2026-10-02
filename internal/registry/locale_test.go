package registry

import "testing"

func TestBestLocale(t *testing.T) {
	s := RealmSettings{Email: &EmailSettings{DefaultLocale: "en", Locales: []string{"en", "es", "pt-BR"}}}
	for _, tt := range []struct {
		tags []string
		want string
	}{
		{nil, "en"},
		{[]string{"es"}, "es"},
		{[]string{"es-MX"}, "es"},    // the language, region dropped
		{[]string{"ES-mx"}, "es"},    // case doesn't matter
		{[]string{"pt-BR"}, "pt-BR"}, // exact beats language
		{[]string{"pt-br"}, "pt-BR"}, // and returns the listed spelling
		{[]string{"pt"}, "en"},       // pt-BR is a region of pt, not the other way round
		{[]string{"pt-PT"}, "en"},
		{[]string{"fr-CA", "es"}, "es"}, // the first tag that matches
		{[]string{"fr", "de"}, "en"},    // nothing matches: the default
		{[]string{"", " es "}, "es"},
		{[]string{"es-MX-x-private"}, "es"},
	} {
		if got := s.BestLocale(tt.tags...); got != tt.want {
			t.Errorf("BestLocale(%v) = %q, want %q", tt.tags, got, tt.want)
		}
	}
	if got := s.ListedLocale("PT-br"); got != "pt-BR" {
		t.Errorf("ListedLocale = %q", got)
	}
	if got := s.ListedLocale("es-MX"); got != "" {
		t.Errorf("an unlisted region is unlisted: %q", got)
	}
	// Without email: English only, and everything maps to it.
	none := RealmSettings{}
	if def, list := none.Languages(); def != "en" || len(list) != 1 || none.BestLocale("es") != "en" {
		t.Errorf("no email: %q %v", def, list)
	}
	// A default other than English.
	es := RealmSettings{Email: &EmailSettings{DefaultLocale: "es", Locales: []string{"es", "en"}}}
	if es.BestLocale("fr") != "es" || es.BestLocale("en-GB") != "en" {
		t.Errorf("default es: %q %q", es.BestLocale("fr"), es.BestLocale("en-GB"))
	}
}
