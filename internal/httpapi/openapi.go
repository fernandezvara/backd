package httpapi

import (
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/openapigen"
	"github.com/fernandezvara/backd/internal/registry"
)

// openAPI serves GET /v1/{realm}/_openapi.json: the OpenAPI 3.1 description of the realm's
// client API, generated from its config. Realm administrators read it; `openapi.public: true`
// in realm.yaml (or a realm without authentication) opens it to everyone.
type openAPI struct {
	reg *registry.Registry
	// protected serves it behind the admin gate, when this instance has an admin API.
	protected http.Handler

	mu   sync.Mutex
	docs map[string]*openapigen.Document
}

func (o *openAPI) document(realm string) (*openapigen.Document, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d, ok := o.docs[realm]; ok {
		return d, nil
	}
	d, err := openapigen.Public(o.reg, realm)
	if err != nil {
		return nil, err
	}
	if o.docs == nil {
		o.docs = map[string]*openapigen.Document{}
	}
	o.docs[realm] = d
	return d, nil
}

func (o *openAPI) serve(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	doc, err := o.document(realm)
	if err != nil {
		notFound(w, r)
		return
	}
	rl := o.reg.Realms[realm]
	etag := `"` + doc.Fingerprint[:32] + `"`
	h := w.Header()
	h.Set("ETag", etag)
	if rl.Settings.OpenAPIPublic || !rl.Settings.AuthEnabled {
		h.Set("Cache-Control", "public, max-age=60")
	} else {
		h.Set("Cache-Control", "private, no-store")
	}
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	_, _ = w.Write(doc.JSON)
}

func (o *openAPI) route(r chi.Router) {
	r.Get("/v1/{realm}/_openapi.json", func(w http.ResponseWriter, req *http.Request) {
		rl, ok := o.reg.Realms[chi.URLParam(req, "realm")]
		if !ok {
			notFound(w, req)
			return
		}
		if rl.Settings.OpenAPIPublic || !rl.Settings.AuthEnabled {
			o.serve(w, req)
			return
		}
		if o.protected == nil {
			notFound(w, req) // this instance serves no admin API
			return
		}
		o.protected.ServeHTTP(w, req)
	})
}
