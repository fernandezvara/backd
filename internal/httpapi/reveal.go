package httpapi

import (
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Who sees an encrypted field (design/field-encryption.md §7). Rules and the code that writes see
// plaintext; only what is answered is redacted, after the read rule has run, by the optional
// `reveal` rule of each field: a hidden field comes back null and is listed in `_meta.redacted`,
// so an app can tell "hidden" from "empty".

type revealMode int

const (
	revealAll  revealMode = iota // every reader sees every field
	revealRule                   // each field's reveal rule decides, per document
	revealNone                   // no field is shown
)

// revealer redacts the documents of one collection for one request. A nil revealer (a collection
// that encrypts nothing) shows everything.
type revealer struct {
	r    *http.Request
	c    *registry.Collection
	mode revealMode
	a    access
	log  *slog.Logger
	// audit says the plaintext is going to an administrator, which is recorded once per request.
	audit    bool
	revealed map[string]bool
	docs     int
	d        *documents
}

// revealerFor decides what the caller of r sees of c's encrypted fields:
//   - callers who bypass the rules (API keys, a function's admin access) and realms without auth
//     see everything, as they do with rules;
//   - the admin data route shows full administrators everything and records it, and read-only
//     administrators nothing;
//   - everyone else gets each field's reveal rule, evaluated on the plain document.
func (d *documents) revealerFor(r *http.Request, c *registry.Collection) *revealer {
	if len(c.Encrypted) == 0 {
		return nil
	}
	rv := &revealer{r: r, c: c, mode: revealAll, log: logger(r.Context()), d: d}
	caller, ok := callerOf(r)
	switch {
	case !ok:
	case adminData(r):
		if adminAccessOf(r).Full() {
			rv.audit, rv.revealed = true, map[string]bool{}
		} else {
			rv.mode = revealNone
		}
	case caller.BypassesRules():
	case len(c.Revealing()) > 0:
		rv.mode, rv.a = revealRule, d.accessIn(r, c)
	}
	return rv
}

// storedView is doc with every encrypted field hidden, whoever asks: for answers kept in the
// system database (idempotency records), which must not hold plaintext.
func (d *documents) storedView(c *registry.Collection, doc map[string]any) (map[string]any, []string) {
	if len(c.Encrypted) == 0 {
		return doc, nil
	}
	return (&revealer{c: c, mode: revealNone, d: d}).view(doc)
}

// view returns doc as the caller may see it and the paths it hides. doc is not modified.
func (rv *revealer) view(doc map[string]any) (map[string]any, []string) {
	if rv == nil {
		return doc, nil
	}
	if rv.mode == revealAll {
		rv.note(doc)
		return doc, nil
	}
	var hidden []string
	for _, ef := range rv.c.Encrypted {
		show := rv.mode == revealRule
		if show && ef.Reveal != nil {
			v := rv.a.values
			v.Document = doc
			ok, err := ef.Reveal.Allow(v)
			if err != nil {
				// A rule that can't be decided hides the field. The error says where, not what.
				rv.log.Warn("a reveal rule failed: the field is hidden", "collection", rv.c.Database+"/"+rv.c.Name, "field", ef.Path, "error", err)
			}
			show = ok && err == nil
		}
		if !show {
			hidden = append(hidden, ef.Path)
		}
	}
	if len(hidden) == 0 {
		return doc, nil
	}
	out := make(map[string]any, len(doc))
	maps.Copy(out, doc)
	for _, p := range hidden {
		nullAt(out, p)
	}
	meta := map[string]any{}
	if m, ok := doc["_meta"].(map[string]any); ok {
		maps.Copy(meta, m)
	}
	sorted := slices.Clone(hidden)
	sort.Strings(sorted)
	list := make([]any, len(sorted))
	for i, p := range sorted {
		list[i] = p
	}
	meta["redacted"] = list
	out["_meta"] = meta
	return out, sorted
}

// note counts a document shown in plain to an administrator, for the audit record.
func (rv *revealer) note(doc map[string]any) {
	if !rv.audit {
		return
	}
	seen := false
	for _, ef := range rv.c.Encrypted {
		if _, ok := docValueAt(doc, ef.Path); ok {
			rv.revealed[ef.Path], seen = true, true
		}
	}
	if seen {
		rv.docs++
	}
}

// flush records, once, that an administrator read encrypted fields in plain: which fields and
// how many documents, never a value.
func (rv *revealer) flush() {
	if rv == nil || !rv.audit || rv.docs == 0 {
		return
	}
	svc := rv.d.users(rv.c.Realm)
	if svc == nil {
		return
	}
	fields := make([]string, 0, len(rv.revealed))
	for f := range rv.revealed {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	svc.Audit(rv.r.Context(), auth.AuditDataReveal, "doc:"+rv.c.Database+"/"+rv.c.Name, map[string]any{
		"database": rv.c.Database, "collection": rv.c.Name, "fields": strings.Join(fields, ","), "documents": rv.docs,
	})
}

// nullAt sets a dot path of doc to null, copying the maps along it so the original is untouched.
func nullAt(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
		}
		cp := make(map[string]any, len(next))
		maps.Copy(cp, next)
		cur[p] = cp
		cur = cp
	}
	cur[parts[len(parts)-1]] = nil
}

// writeViewed answers with a document as the caller may see it.
func (d *documents) writeViewed(w http.ResponseWriter, r *http.Request, c *registry.Collection, status int, doc map[string]any) {
	shown, _ := d.revealerFor(r, c).view(doc)
	writeDocument(w, status, shown)
}
