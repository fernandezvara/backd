package httpapi

import (
	"context"
	"net/http"

	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
)

// The `fields` parameter (design/field-projection.md): the response keeps `id` and the named
// paths. It is parsed before anything is written, travels in the request's context, and is
// applied last, to the document as the caller sees it.

type selectionKey struct{}

// withSelection parses `fields` and returns the request carrying it. It answers 400 when a path
// is not a field of the collection (ok is false then).
func (d *documents) withSelection(w http.ResponseWriter, r *http.Request, c *registry.Collection) (*http.Request, *selection, bool) {
	sel, err := query.ParseFields(r.URL.Query().Get("fields"), c.Fields)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", queryDetails(err)...)
		return r, nil, false
	}
	s := &selection{sel}
	return r.WithContext(context.WithValue(r.Context(), selectionKey{}, s)), s, true
}

// selectionOf is the selection withSelection put in the request; nil (keep everything) without one.
func selectionOf(r *http.Request) *selection {
	s, _ := r.Context().Value(selectionKey{}).(*selection)
	return s
}

// selection wraps query.Selection with what the handlers need; a nil *selection keeps everything.
type selection struct{ s *query.Selection }

// Apply reduces one rendered document.
func (s *selection) Apply(doc map[string]any) map[string]any {
	if s == nil || s.s == nil {
		return doc
	}
	return s.s.Apply(doc)
}

// withKey adds the selection to the parts of a validator's representation tag.
func (s *selection) withKey(parts []string) []string {
	if s == nil || s.s == nil {
		return parts
	}
	return append(parts, s.s.Key())
}

// applyToPage reduces the items of a list answer.
func (s *selection) applyToPage(resp listResponse) listResponse {
	if s == nil || s.s == nil {
		return resp
	}
	items := make([]map[string]any, len(resp.Items))
	for i, it := range resp.Items {
		items[i] = s.s.Apply(it)
	}
	resp.Items = items
	return resp
}
