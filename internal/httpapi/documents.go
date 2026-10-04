package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// timeFormat renders _meta timestamps: RFC3339, UTC, millisecond precision
	// (MongoDB's date precision).
	timeFormat = "2006-01-02T15:04:05.000Z07:00"
)

// Store gives access to the repository of each configured collection.
type Store interface {
	Repository(c *registry.Collection) storage.Repository
	// Transact runs fn once; every write made through a Repository, using
	// the context fn receives, either all commit or none do (batch writes,
	// roadmap F7). fn may run more than once on a transient conflict, so
	// it must have no side effects beyond the repositories it's given.
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

type collectionKey struct{}

type documents struct {
	reg       *registry.Registry
	store     Store
	now       func() time.Time
	maxBody   int64
	opTimeout time.Duration
	// users returns the user service of an auth-enabled realm, or nil.
	users func(realm string) *auth.Users
	// internal: served on the internal listener, to functions calling back
	// with callback tokens signed with callbackKey (and nothing else).
	internal    bool
	callbackKey []byte
}

func (d *documents) routes(r chi.Router) {
	r.Route("/v1/{realm}/{database}/{collection}", func(r chi.Router) {
		r.Use(dataCaching, d.resolveCollection, withTimeout(d.opTimeout), d.authorize)
		json := requireContentType("application/json")
		mergePatch := requireContentType("application/merge-patch+json")
		r.With(json).Post("/", d.create)
		r.Get("/", d.list)
		r.Get("/{id}", d.get)
		r.With(json).Put("/{id}", d.replace)
		r.With(mergePatch).Patch("/{id}", d.patch)
		r.Delete("/{id}", d.delete)
	})
	r.With(noStore, withTimeout(d.opTimeout), requireContentType("application/json")).Post("/v1/{realm}/{database}/_batch", d.batch)
}

// resolveCollection looks up the collection in the registry, or answers 404.
func (d *documents) resolveCollection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := d.reg.Collection(chi.URLParam(r, "realm"), chi.URLParam(r, "database"), chi.URLParam(r, "collection"))
		if !ok {
			notFound(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), collectionKey{}, c)))
	})
}

// withTimeout puts a deadline on the request's storage (and hashing) work.
func withTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (d *documents) collection(r *http.Request) (*registry.Collection, storage.Repository) {
	c := r.Context().Value(collectionKey{}).(*registry.Collection)
	return c, d.store.Repository(c)
}

func (d *documents) create(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	claim, ok := d.claimKey(w, r, c.Name, body) // Idempotency-Key, if the request has one
	if !ok {
		return
	}
	defer claim.release(r.Context())
	stripSystemFields(body)
	if !validate(w, r, c, body) {
		return
	}
	doc := c.DatesToStorage(jsonnum.Normalize(body).(map[string]any))
	if !allowWrite(w, r, d.accessFor(r), c, rules.Create, nil, doc) {
		return
	}

	now := d.timestamp()
	doc["id"] = xid.New().String()
	meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
	stampCreate(r, meta)
	doc["_meta"] = meta
	if err := repo.Create(r.Context(), doc); err != nil {
		storageError(w, r, err)
		return
	}
	w.Header().Set("Location", r.URL.JoinPath(url.PathEscape(doc["id"].(string))).Path)
	out := render(doc)
	claim.complete(r.Context(), http.StatusCreated, out)
	w.Header().Set("ETag", etag(version(doc)))
	writeJSON(w, http.StatusCreated, out)
}

func (d *documents) get(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	filter, ok := readFilter(w, r, d.accessFor(r), c)
	if !ok {
		return
	}
	doc, err := storage.Fetch(r.Context(), repo, chi.URLParam(r, "id"), filter)
	if err != nil {
		storageError(w, r, err)
		return
	}
	writeDocument(w, http.StatusOK, doc)
}

type listResponse struct {
	Items   []map[string]any `json:"items"`
	Limit   int              `json:"limit"`
	Skip    int              `json:"skip"`
	HasMore bool             `json:"has_more"`
	// NextCursor continues the list (`after`): set when HasMore, unless the
	// order_by can't be followed by a cursor (arrays, objects, mixed types).
	NextCursor string `json:"next_cursor,omitempty"`
	Total      *int64 `json:"total,omitempty"`
}

func (d *documents) list(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	q, details := parseListQuery(r.URL.Query(), c.Fields)
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}
	filter, ok := readFilter(w, r, d.accessFor(r), c)
	if !ok {
		return
	}
	q.Access = filter
	page, err := repo.List(r.Context(), q)
	if err != nil {
		storageError(w, r, err)
		return
	}
	resp := listResponse{Items: make([]map[string]any, 0, len(page.Items)), Limit: q.Limit, Skip: q.Skip, HasMore: page.HasMore, Total: page.Total}
	for _, doc := range page.Items {
		resp.Items = append(resp.Items, render(doc))
	}
	if page.HasMore && len(page.Items) > 0 {
		if ok, _ := query.Cursorable(q.Sort, c.Fields); ok {
			resp.NextCursor = query.EncodeCursor(q.Sort, page.Items[len(page.Items)-1])
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

var listParams = map[string]bool{"where": true, "order_by": true, "limit": true, "skip": true, "after": true, "count": true}

func parseListQuery(v url.Values, fields map[string]registry.Field) (storage.Query, []Detail) {
	q := storage.Query{Limit: defaultLimit}
	var details []Detail
	for key, vals := range v {
		switch {
		case !listParams[key]:
			details = append(details, Detail{Path: key, Reason: "unknown query parameter"})
		case len(vals) > 1:
			details = append(details, Detail{Path: key, Reason: "must be given at most once"})
		}
	}
	addErr := func(err error) {
		var qe query.Errors
		if errors.As(err, &qe) {
			for _, e := range qe {
				details = append(details, Detail{Path: e.Path, Reason: e.Reason})
			}
		}
	}
	var err error
	if q.Filter, err = query.ParseWhere(v.Get("where"), fields); err != nil {
		addErr(err)
	}
	if q.Sort, err = query.ParseOrderBy(v.Get("order_by"), fields); err != nil {
		addErr(err)
	}
	if q.Count, err = query.ParseCount(v.Get("count")); err != nil {
		addErr(err)
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		q.Limit = n
	}
	if s := v.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		q.Skip = n
	}
	if cursor := v.Get("after"); v.Has("after") {
		switch {
		case v.Has("skip"):
			details = append(details, Detail{Path: "after", Reason: "can't be combined with skip: a cursor already says where the page starts"})
		case q.Sort != nil:
			var err error
			if q.After, err = query.ParseCursor(cursor, q.Sort, fields); err != nil {
				addErr(err)
			}
		}
	}
	return q, details
}

func (d *documents) replace(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	stripSystemFields(body)
	d.write(w, r, c, repo, func(map[string]any) (map[string]any, bool) {
		if !validate(w, r, c, body) {
			return nil, false
		}
		return c.DatesToStorage(jsonnum.Normalize(userFields(body)).(map[string]any)), true
	})
}

func (d *documents) patch(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	patch, ok := readObject(w, r)
	if !ok {
		return
	}
	stripSystemFields(patch)
	d.write(w, r, c, repo, func(current map[string]any) (map[string]any, bool) {
		// Deep copies: merging must not modify the stored document, which the
		// update rule (changed()) compares against.
		merged := mergePatch(timesToStrings(userFields(current)).(map[string]any), deepCopy(patch))
		if !validate(w, r, c, merged) {
			return nil, false
		}
		return c.DatesToStorage(jsonnum.Normalize(merged).(map[string]any)), true
	})
}

// maxWriteAttempts bounds the read-modify-write retries of PUT and PATCH.
const maxWriteAttempts = 3

// write runs the conditional read-modify-write cycle shared by PUT and
// PATCH. build returns the new user fields for the current document, or
// false after writing an error response. Every write is conditional on the
// version read. When the document changes underneath, a request with a
// specific If-Match fails with 412; otherwise the cycle is retried, and
// gives up with 409 write_conflict after maxWriteAttempts.
func (d *documents) write(w http.ResponseWriter, r *http.Request, c *registry.Collection, repo storage.Repository, build func(current map[string]any) (map[string]any, bool)) {
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	a := d.accessFor(r)
	filter, ok := readFilter(w, r, a, c)
	if !ok {
		return
	}
	for attempt := range maxWriteAttempts {
		if attempt > 0 && !backoff(r.Context(), attempt) {
			storageError(w, r, r.Context().Err())
			return
		}
		current, err := storage.Fetch(r.Context(), repo, chi.URLParam(r, "id"), filter)
		if err != nil {
			storageError(w, r, err)
			return
		}
		read := version(current)
		if !cond.matches(read) {
			versionMismatch(w, r, read)
			return
		}
		fields, ok := build(current)
		if !ok {
			return
		}
		if !allowWrite(w, r, a, c, rules.Update, current, fields) {
			return
		}
		stored, _ := current["_meta"].(map[string]any)
		meta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": read + 1}
		stampUpdate(r, stored, meta)
		fields["id"] = current["id"]
		fields["_meta"] = meta

		err = repo.Replace(r.Context(), fields, read)
		switch {
		case err == nil:
			writeDocument(w, http.StatusOK, fields)
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
			logger(r.Context()).Debug("write conflict, retrying", "id", current["id"], "version", read)
		default:
			storageError(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

// backoff sleeps a random time that grows with the attempt number, so
// concurrent writers to one document spread out. It returns false if the
// request context ends first.
func backoff(ctx context.Context, attempt int) bool {
	t := time.NewTimer(rand.N(time.Duration(attempt) * 10 * time.Millisecond))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *documents) delete(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	id := chi.URLParam(r, "id")
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	a := d.accessFor(r)
	filter, ok := readFilter(w, r, a, c)
	if !ok {
		return
	}

	// Without rules or If-Match, the document needn't be read first.
	if !a.ruled && !cond.specific() {
		if err := repo.Delete(r.Context(), id, nil); err != nil {
			storageError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Otherwise the delete is conditional on the version that was checked,
	// so the document can't change between the check and the delete.
	for attempt := range maxWriteAttempts {
		if attempt > 0 && !backoff(r.Context(), attempt) {
			storageError(w, r, r.Context().Err())
			return
		}
		current, err := storage.Fetch(r.Context(), repo, id, filter)
		if err != nil {
			storageError(w, r, err)
			return
		}
		read := version(current)
		if !cond.matches(read) {
			versionMismatch(w, r, read)
			return
		}
		if !allowWrite(w, r, a, c, rules.Delete, current, nil) {
			return
		}
		switch err := repo.Delete(r.Context(), id, &read); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
			logger(r.Context()).Debug("delete conflict, retrying", "id", id, "version", read)
		default:
			storageError(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

func versionMismatch(w http.ResponseWriter, r *http.Request, current int64) {
	w.Header().Set("ETag", etag(current))
	writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "If-Match does not match the current version "+etag(current))
}

// deepCopy copies nested maps and slices so a patch can be re-applied on retry.
func deepCopy(m map[string]any) map[string]any {
	var cp func(v any) any
	cp = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[k] = cp(e)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = cp(e)
			}
			return out
		}
		return v
	}
	return cp(m).(map[string]any)
}

func (d *documents) timestamp() time.Time {
	return d.now().UTC().Truncate(time.Millisecond)
}

// readObject decodes the request body as a single JSON object, keeping
// numbers as json.Number. It answers 400 invalid_json otherwise.
func readObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	data, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return nil, false
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON: "+err.Error())
		return nil, false
	}
	if dec.More() {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body must contain a single JSON value")
		return nil, false
	}
	obj, ok := v.(map[string]any)
	if !ok {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body must be a JSON object")
		return nil, false
	}
	return obj, true
}

// validate checks doc against the collection schema, answering 400 on failure.
func validate(w http.ResponseWriter, r *http.Request, c *registry.Collection, doc map[string]any) bool {
	if err := c.Schema.Validate(doc); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "document failed schema validation", validationDetails(err)...)
		return false
	}
	return true
}

// stripSystemFields drops client-supplied system fields, which are always ignored.
func stripSystemFields(doc map[string]any) {
	delete(doc, "id")
	delete(doc, "_id")
	delete(doc, "_meta")
}

// userFields returns a copy of doc without system fields.
func userFields(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	stripSystemFields(out)
	return out
}

// version returns the document's _meta.version, or 0 for documents stored
// before versioning existed.
func version(doc map[string]any) int64 {
	meta, _ := doc["_meta"].(map[string]any)
	v, _ := meta["version"].(int64)
	return v
}

// etag renders a document version as a strong entity tag.
func etag(v int64) string {
	return `"` + strconv.FormatInt(v, 10) + `"`
}

// writeDocument answers with a single document and its ETag.
func writeDocument(w http.ResponseWriter, status int, doc map[string]any) {
	w.Header().Set("ETag", etag(version(doc)))
	writeJSON(w, status, render(doc))
}

// render prepares a stored document for the response, formatting _meta timestamps.
func render(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	// Every time becomes text: _meta's timestamps, and the fields stored as dates.
	for k, v := range out {
		out[k] = timesToStrings(v)
	}
	return out
}

// storageError maps repository errors to HTTP responses.
func storageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "document not found")
	case errors.Is(err, storage.ErrConflict):
		var details []Detail
		var ce *storage.ConflictError
		if errors.As(err, &ce) {
			reason := "must be unique"
			if len(ce.Fields) > 1 {
				reason = "must be unique in combination with " + strings.Join(ce.Fields, ", ")
			}
			for _, f := range ce.Fields {
				details = append(details, Detail{Path: f, Reason: reason})
			}
		}
		writeError(w, r, http.StatusConflict, codeConflict, "a document with the same unique value already exists", details...)
	case errors.Is(err, storage.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		logger(r.Context()).Error("storage unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "storage unavailable")
	default:
		logger(r.Context()).Error("storage error", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
