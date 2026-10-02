package registry

import (
	"slices"
	"strings"
)

// Languages returns a realm's default language and the languages it
// supports: email.default_locale and email.locales, or just English when it
// configures no email.
func (s RealmSettings) Languages() (def string, list []string) {
	if s.Email != nil {
		return s.Email.DefaultLocale, s.Email.Locales
	}
	return DefaultLocale, []string{DefaultLocale}
}

// ListedLocale returns the listed language equal to tag, ignoring case (es-mx
// finds es-MX), or "" when the realm doesn't list it.
func (s RealmSettings) ListedLocale(tag string) string {
	_, list := s.Languages()
	i := slices.IndexFunc(list, func(l string) bool { return strings.EqualFold(l, strings.TrimSpace(tag)) })
	if i < 0 {
		return ""
	}
	return list[i]
}

// BestLocale maps the languages a person asked for, most wanted first, to the
// best language the realm lists, without ever failing: an exact match (es-MX),
// then the same language with its region dropped (es), then the default. The
// first tag that matches either way wins, so ["fr-CA", "es"] on a realm that
// lists es and en gives es.
func (s RealmSettings) BestLocale(tags ...string) string {
	def, _ := s.Languages()
	for _, tag := range tags {
		for tag = strings.TrimSpace(tag); tag != ""; tag = parentTag(tag) {
			if l := s.ListedLocale(tag); l != "" {
				return l
			}
		}
	}
	return def
}

// parentTag drops the last subtag: es-MX -> es, es -> "".
func parentTag(tag string) string {
	i := strings.LastIndex(tag, "-")
	if i < 0 {
		return ""
	}
	return tag[:i]
}
