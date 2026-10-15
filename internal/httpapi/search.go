package httpapi

import (
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeSearchNotEnabled = "search_not_enabled"
	codeIndexBuilding    = "index_building"
)

// prepareSearch checks that the collection can be searched and gives the search its language.
// It answers 400 search_not_enabled and returns false when there is no text index.
func (d *documents) prepareSearch(w http.ResponseWriter, r *http.Request, c *registry.Collection, s *storage.Search) bool {
	if c.Text == nil {
		writeError(w, r, http.StatusBadRequest, codeSearchNotEnabled, "this collection has no text index: declare {\"text\": {…}} in its indexes.json to search it")
		return false
	}
	if s != nil {
		s.Language = d.searchLanguage(r)
	}
	return true
}

// searchLanguage is the language a query is stemmed in: `language=` when given, else the
// signed-in user's locale, else the first language of Accept-Language for anonymous callers,
// else "" (the index's default). Locale codes are mapped to the database's languages the way
// documents are when they are indexed.
func (d *documents) searchLanguage(r *http.Request) string {
	if l := strings.TrimSpace(r.URL.Query().Get("language")); l != "" {
		return registry.SearchLanguage(l)
	}
	if c, ok := callerOf(r); ok && c.User != nil {
		if l := c.User.User.Locale; l != "" {
			return registry.SearchLanguage(l)
		}
		return ""
	}
	if h := r.Header.Get("Accept-Language"); h != "" {
		first, _, _ := strings.Cut(h, ",")
		first, _, _ = strings.Cut(first, ";")
		if first = strings.TrimSpace(first); first != "" && first != "*" {
			return registry.SearchLanguage(first)
		}
	}
	return ""
}
