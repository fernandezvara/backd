package httpapi

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
)

// consentRoutes mounts the public consent texts: /v1/{realm}/_consents. They
// need no session, because a person reads them before they have an account.
func (a *authAPI) consentRoutes(r chi.Router) {
	r.Route("/v1/{realm}/_consents", func(r chi.Router) {
		r.Use(a.resolveRealm)
		r.Get("/", a.listConsents)
		r.Get("/{name}/{version}", a.consentText)
	})
}

// consentCache lets browsers and CDNs keep the texts for a few minutes: they change
// only with a deploy, and a version's text never changes at all.
const consentCache = "public, max-age=300"

type consentVersionInfo struct {
	Version    string  `json:"version"`
	Reaccept   bool    `json:"reaccept"`
	ReacceptBy *string `json:"reaccept_by,omitempty"`
}

// listConsents answers GET /v1/{realm}/_consents: the consents the realm asks for
// and the version in force of each, with the languages its text exists in.
func (a *authAPI) listConsents(w http.ResponseWriter, r *http.Request) {
	s := usersOf(r).Settings
	items := []map[string]any{}
	for _, name := range s.ConsentNames() {
		c := s.Consents[name]
		cur := c.Versions[c.Current]
		info := consentVersionInfo{Version: c.Current, Reaccept: cur.Reaccept}
		if !cur.ReacceptBy.IsZero() {
			d := cur.ReacceptBy.Format("2006-01-02")
			info.ReacceptBy = &d
		}
		locales := make([]string, 0, len(c.Texts[c.Current]))
		for l := range c.Texts[c.Current] {
			locales = append(locales, l)
		}
		slices.Sort(locales)
		items = append(items, map[string]any{"name": name, "required": c.Required, "current": info, "locales": locales})
	}
	w.Header().Set("Cache-Control", consentCache)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// consentText answers GET /v1/{realm}/_consents/{name}/{version}?locale=: the exact
// text of a version in a language (the realm's best match for it), with its hash,
// which is what a consent record keeps as proof.
func (a *authAPI) consentText(w http.ResponseWriter, r *http.Request) {
	s := usersOf(r).Settings
	c := s.Consents[chi.URLParam(r, "name")]
	version := chi.URLParam(r, "version")
	if c == nil || len(c.Texts[version]) == 0 {
		writeError(w, r, http.StatusNotFound, codeNotFound, "consent version not found")
		return
	}
	locale := s.BestLocale(r.URL.Query().Get("locale"))
	text, ok := c.Texts[version][locale]
	if !ok {
		// A version with texts only in some languages: the realm's default.
		locale, _ = s.Languages()
		if text, ok = c.Texts[version][locale]; !ok {
			writeError(w, r, http.StatusNotFound, codeNotFound, "consent version not found in that language")
			return
		}
	}
	w.Header().Set("Cache-Control", consentCache)
	w.Header().Set("Content-Language", locale)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": c.Name, "version": version, "locale": locale, "text": text.Body, "hash": text.Hash,
		"current": version == c.Current,
	})
}
