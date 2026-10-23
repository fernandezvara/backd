package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/realtime"
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
	reg            *registry.Registry
	store          Store
	objects        *realmObjects            // each realm's object storage (files)
	baseURL        string                   // backd's public address, for the links it signs (BACKD_URL)
	imageMaxPixels int64                    // the instance's image pixel limit (BACKD_IMAGE_MAX_PIXELS)
	expand         registry.ExpandLimits    // reference expansion limits (BACKD_EXPAND_MAX_*), defaults filled in
	aggregateLim   registry.AggregateLimits // aggregation limits (BACKD_AGGREGATE_*), defaults filled in
	maxUpload      int64                    // the largest file a proxy upload may carry (BACKD_MAX_UPLOAD_BYTES)
	now            func() time.Time
	maxBody        int64
	opTimeout      time.Duration
	// users returns the user service of an auth-enabled realm, or nil.
	users func(realm string) *auth.Users
	// internal: served on the internal listener, to functions calling back
	// with callback tokens signed with callbackKey (and nothing else).
	internal    bool
	callbackKey []byte

	// hub serves realtime streams (nil when this instance has none); realtimeLimits are the
	// instance's. processKey signs resume tokens of realms without system data.
	hub            *realtime.Hub
	realtimeLimits realtime.Limits
	procKey        []byte
	procOnce       sync.Once
	// dups limits the creates of custom ids that exist, per caller.
	dups    dupLimiter
	metrics *metrics.Metrics
}

func (d *documents) routes(r chi.Router) {
	r.Route("/v1/{realm}/{database}/{collection}", func(r chi.Router) {
		r.Use(dataCaching, d.resolveCollection, withTimeout(d.opTimeout), d.authorize)
		d.mountDocuments(r)
		// Summaries are for apps and functions; the admin data route has no use for them.
		r.Get("/_aggregate", d.aggregate)
	})
	r.With(noStore, withTimeout(d.opTimeout), requireContentType("application/json")).Post("/v1/{realm}/{database}/_batch", d.batch)
	r.With(dataCaching, withTimeout(d.opTimeout)).Get("/v1/{realm}/_config", d.readAppConfig)
	if !d.internal {
		// No timeout and no body type check: a stream lives as long as its client does.
		r.With(noStore, requireContentType("application/json")).Post("/v1/{realm}/_realtime", d.realtime)
	}
}

// mountDocuments registers the document operations of one collection: the
// data routes, and the admin data route (which brings its own authentication).
func (d *documents) mountDocuments(r chi.Router) {
	json := requireContentType("application/json")
	mergePatch := requireContentType("application/merge-patch+json")
	r.With(json).Post("/", d.create)
	r.Get("/", d.list)
	r.Get("/{id}", d.get)
	r.With(json).Put("/{id}", d.replace)
	r.With(mergePatch).Patch("/{id}", d.patch)
	r.Delete("/{id}", d.delete)
	r.Post("/{id}/restore", d.restore)
	d.mountFiles(r)
}

// The admin data route (/_admin/data/…) serves the same operations to an
// administrator, past the collections' rules: the request is flagged, so the
// documents are read and written unruled, a created document has no owner, and
// each write is audited (database, collection and id, never content).
type adminDataKey struct{}

func adminData(r *http.Request) bool {
	v, _ := r.Context().Value(adminDataKey{}).(bool)
	return v
}

// auditData records a write made through the admin data route.
func (d *documents) auditData(r *http.Request, action string, c *registry.Collection, id string) {
	if !adminData(r) {
		return
	}
	if svc := d.users(c.Realm); svc != nil {
		svc.Audit(r.Context(), action, "doc:"+c.Database+"/"+c.Name+"/"+id, map[string]any{"database": c.Database, "collection": c.Name, "id": id})
	}
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
	claim, ok := d.claimKey(w, r, c.Name, body, len(c.Encrypted) > 0) // Idempotency-Key, if the request has one
	if !ok {
		return
	}
	defer claim.release(r.Context())
	id, problem := newID(c, r, body)
	if problem != nil {
		writeError(w, r, problem.status, problem.code, problem.reason, Detail{Path: "id", Reason: problem.reason})
		return
	}
	org, team, ok := d.createScope(w, r, c) // membership comes before the create rule
	if !ok {
		return
	}
	stripSystemFields(body)
	refs, details := takeFileRefs(c, body)
	if len(details) > 0 {
		refsError(w, r, details)
		return
	}
	var pending *pendingSet
	if len(refs) > 0 {
		if pending, ok = d.resolvePending(w, r, c, refs); !ok {
			return
		}
		if !pending.apply(w, r, c, body, nil) {
			return
		}
	}
	if !validate(w, r, c, body) {
		return
	}
	doc := c.DatesToStorage(jsonnum.Normalize(body).(map[string]any))
	if id != "" {
		doc["id"] = id // the create rule sees data.id
	}
	if scope := scopeMeta(org, team); len(scope) > 0 {
		doc["_meta"] = scope // ...and data._meta.org and data._meta.team
	}
	if !allowWrite(w, r, d.accessFor(r), c, rules.Create, nil, doc) {
		return
	}
	if d.dupBlocked(w, r, c) {
		return
	}

	now := d.timestamp()
	if id == "" {
		doc["id"] = xid.New().String()
	}
	meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
	stampCreate(r, meta)
	maps.Copy(meta, scopeMeta(org, team))
	doc["_meta"] = meta
	if pending != nil && !pending.claim(w, r, doc["id"].(string)) {
		return
	}
	if err := repo.Create(r.Context(), doc); err != nil {
		if pending != nil {
			pending.release(r)
		}
		if id != "" && isIDConflict(err) {
			d.idTaken(w, r, c, "a document with this id already exists", Detail{Path: "id", Reason: "already exists"})
			return
		}
		storageError(w, r, err)
		return
	}
	if pending != nil {
		pending.attached(r, d, c, doc["id"].(string), docOwner(doc))
	}
	w.Header().Set("Location", r.URL.JoinPath(url.PathEscape(doc["id"].(string))).Path)
	shown, _ := d.revealerFor(r, c).view(doc)
	out := render(shown)
	d.auditData(r, auth.AuditDataCreate, c, doc["id"].(string))
	// The remembered answer lives in the system database: it never holds an encrypted field in plain.
	kept, _ := d.storedView(c, doc)
	claim.complete(r.Context(), http.StatusCreated, render(kept))
	w.Header().Set("ETag", etag(version(doc)))
	writeJSON(w, http.StatusCreated, out)
}

func (d *documents) get(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	view, ok := parseTrash(w, r, c)
	if !ok {
		return
	}
	filter, ok := viewFilter(w, r, d.accessFor(r), c, view)
	if !ok {
		return
	}
	doc, err := storage.Fetch(r.Context(), repo, chi.URLParam(r, "id"), filter)
	if err != nil {
		storageError(w, r, err)
		return
	}
	plan, ok := d.expandPlanOf(w, r, c)
	if !ok {
		return
	}
	rv := d.revealerFor(r, c)
	shown, redacted := rv.view(doc)
	rv.flush()
	var out map[string]any
	if plan != nil {
		out = render(shown)
		if !d.expandInto(w, r, plan, []storage.Document{doc}, []map[string]any{out}) {
			return
		}
	}
	if r.URL.Query().Get("file_links") == "true" && len(c.Files) > 0 {
		if out == nil {
			out = render(shown)
		}
		if !d.addFileLinks(w, r, c, []map[string]any{out}) {
			return
		}
		// Links are made per response and expire: not part of the version, so
		// this answer is never 304 and never stored.
		w.Header().Set("ETag", etag(version(doc)))
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, out)
		return
	}
	if plan != nil {
		// What was expanded depends on the caller and on other documents: the ETag stays the
		// document's own version (If-Match keeps working), but the response is never cached
		// and never 304.
		w.Header().Set("ETag", etag(version(doc)))
		w.Header().Set("Cache-Control", "private, no-cache")
		writeJSON(w, http.StatusOK, out)
		return
	}
	// What the caller may not see of the encrypted fields is part of the tag, so a caller whose
	// reveal outcome changes never gets a stale 304; If-Match still compares the version only.
	tag := docETag(version(doc), representationTag(redacted))
	readHeaders(w, tag)
	d.publicCache(w, r, c, view)
	if notModified(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, render(shown))
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
	details = explainReferenceFilters(c, details)
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, queryCode(details), "invalid query parameters", details...)
		return
	}
	if q.Search != nil || q.Relevance {
		if !d.prepareSearch(w, r, c, q.Search) {
			return
		}
		q.MaxTime = d.aggregateLim.Time // a search that needs more is refused, as an aggregation is
	}
	view, ok := parseTrash(w, r, c)
	if !ok {
		return
	}
	// ?org= and ?team= are shorthands for a where on _meta.org and _meta.team; the read rule
	// still decides what is seen.
	if q.Filter, ok = scopeFilter(w, r, c, q.Filter); !ok {
		return
	}
	filter, ok := viewFilter(w, r, d.accessFor(r), c, view)
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
	rv := d.revealerFor(r, c)
	var hidden []string
	for _, doc := range page.Items {
		shown, red := rv.view(doc)
		for _, p := range red {
			hidden = append(hidden, doc["id"].(string)+":"+p)
		}
		resp.Items = append(resp.Items, render(shown))
	}
	rv.flush()
	plan, ok := d.expandPlanOf(w, r, c)
	if !ok {
		return
	}
	if plan != nil && !d.expandInto(w, r, plan, page.Items, resp.Items) {
		return
	}
	if page.HasMore && len(page.Items) > 0 && !q.Relevance {
		if ok, _ := query.Cursorable(q.Sort, c.Fields); ok {
			resp.NextCursor = query.EncodeCursor(q.Sort, page.Items[len(page.Items)-1])
		}
	}
	if r.URL.Query().Get("file_links") == "true" && len(c.Files) > 0 {
		// Links are made per response and expire: never 304, never stored.
		if !d.addFileLinks(w, r, c, resp.Items) {
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if plan != nil {
		// Never cached and never 304: see get. A list's validator would not cover the
		// expanded documents, so there is none.
		w.Header().Set("Cache-Control", "private, no-cache")
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// The query and the rules have run: the validator covers what this caller sees.
	tag := listETag(page.Items, representationTag(hidden), resp.HasMore, resp.NextCursor, resp.Total)
	readHeaders(w, tag)
	d.publicCache(w, r, c, view)
	if notModified(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

var listParams = map[string]bool{"language": true, "expand": true, "where": true, "order_by": true, "limit": true, "skip": true, "after": true, "count": true, "deleted": true, "file_links": true, "org": true, "team": true}

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
				details = append(details, Detail{Path: e.Path, Reason: e.Reason, Code: e.Code})
			}
		}
	}
	var err error
	var text string
	if q.Filter, text, err = query.ParseWhereSearch(v.Get("where"), fields); err != nil {
		addErr(err)
	}
	if text != "" {
		q.Search = &storage.Search{Text: text}
	}
	orderBy := v.Get("order_by")
	if relevanceSort(orderBy) {
		q.Relevance, orderBy = true, ""
		switch {
		case text == "":
			details = append(details, Detail{Path: "order_by", Reason: "_relevance needs a $search in where: it orders by how well documents match it"})
		case v.Has("after"):
			details = append(details, Detail{Path: "after", Reason: "can't be combined with order_by=_relevance: relevance pages with skip"})
		}
	} else if strings.Contains(orderBy, "_relevance") {
		details = append(details, Detail{Path: "order_by", Reason: "_relevance can't be combined with other fields: it orders by the search's score alone (ties by id)"})
		orderBy = ""
	}
	if q.Sort, err = query.ParseOrderBy(orderBy, fields); err != nil {
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
	if q.Relevance && q.Skip+q.Limit > maxRelevanceResults {
		details = append(details, Detail{Path: "skip", Reason: "relevance pages reach at most the first " + strconv.Itoa(maxRelevanceResults) + " results: narrow the search or its where"})
	}
	if cursor := v.Get("after"); v.Has("after") && !q.Relevance {
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

// takePending takes the file fields out of a PUT or PATCH body and resolves the
// pending uploads it names, if any: nil when it names none. It answers and returns
// false when it can't.
func (d *documents) takePending(w http.ResponseWriter, r *http.Request, c *registry.Collection, body map[string]any) (*pendingSet, bool) {
	refs, details := takeFileRefs(c, body)
	if len(details) > 0 {
		refsError(w, r, details)
		return nil, false
	}
	if len(refs) == 0 {
		return nil, true
	}
	return d.resolvePending(w, r, c, refs)
}

func (d *documents) replace(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	claimed := claimedScope(c, body)
	stripSystemFields(body)
	pending, ok := d.takePending(w, r, c, body)
	if !ok {
		return
	}
	d.write(w, r, c, repo, pending, func(current map[string]any) (map[string]any, bool) {
		if !sameScope(w, r, claimed, current) {
			return nil, false
		}
		candidate := body
		if len(c.Files) > 0 { // a PUT keeps the document's files
			candidate = deepCopy(body)
			keepFileFields(c, candidate, timesToStrings(current).(map[string]any))
		}
		if pending != nil && !pending.apply(w, r, c, candidate, timesToStrings(current).(map[string]any)) {
			return nil, false
		}
		if !validate(w, r, c, candidate) {
			return nil, false
		}
		return c.DatesToStorage(jsonnum.Normalize(userFields(candidate)).(map[string]any)), true
	})
}

func (d *documents) patch(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	patch, ok := readObject(w, r)
	if !ok {
		return
	}
	claimed := claimedScope(c, patch)
	stripSystemFields(patch)
	pending, ok := d.takePending(w, r, c, patch)
	if !ok {
		return
	}
	d.write(w, r, c, repo, pending, func(current map[string]any) (map[string]any, bool) {
		if !sameScope(w, r, claimed, current) {
			return nil, false
		}
		// Deep copies: merging must not modify the stored document, which the
		// update rule (changed()) compares against.
		merged := mergePatch(timesToStrings(userFields(current)).(map[string]any), deepCopy(patch))
		if pending != nil && !pending.apply(w, r, c, merged, timesToStrings(current).(map[string]any)) {
			return nil, false
		}
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
func (d *documents) write(w http.ResponseWriter, r *http.Request, c *registry.Collection, repo storage.Repository, pending *pendingSet, build func(current map[string]any) (map[string]any, bool)) {
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

		if pending != nil && !pending.claim(w, r, current["id"].(string)) {
			return
		}
		err = repo.Replace(r.Context(), fields, read)
		if err != nil && pending != nil {
			pending.release(r)
		}
		switch {
		case err == nil:
			if pending != nil {
				pending.attached(r, d, c, fields["id"].(string), docOwner(current))
			}
			d.auditData(r, auth.AuditDataUpdate, c, fields["id"].(string))
			d.writeViewed(w, r, c, http.StatusOK, fields)
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
	if r.URL.Query().Has("purge") {
		d.purge(w, r, c, repo)
		return
	}
	if c.SoftDelete != nil {
		d.softDelete(w, r, c, repo)
		return
	}
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
	if !a.ruled && !cond.specific() && len(c.Files) == 0 {
		if err := repo.Delete(r.Context(), id, nil); err != nil {
			storageError(w, r, err)
			return
		}
		d.auditData(r, auth.AuditDataDelete, c, id)
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
			d.documentGone(r.Context(), c, current)
			d.auditData(r, auth.AuditDataDelete, c, id)
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
	delete(doc, "_expanded")
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
	case errors.Is(err, storage.ErrSearchTooExpensive):
		writeError(w, r, http.StatusUnprocessableEntity, codeSearchTooCostly, "this search needs more time than allowed: use fewer or rarer words, or narrow `where`")
	case errors.Is(err, storage.ErrIndexBuilding):
		w.Header().Set("Retry-After", "5")
		writeError(w, r, http.StatusServiceUnavailable, codeIndexBuilding, "the text index of this collection is being rebuilt: try again in a few seconds")
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
	case errors.Is(err, storage.ErrFieldEncrypted):
		writeError(w, r, http.StatusBadRequest, codeFieldEncrypted, "the query uses an encrypted field in a way its blind index can't answer: equality and `in` on a field with `search: equality` are the only searches")
	case errors.Is(err, fieldcrypt.ErrUnavailable), errors.Is(err, fieldcrypt.ErrCorrupt):
		// Never the field's value: the errors carry only where the problem is.
		logger(r.Context()).Error("encrypted field", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "an encrypted field can't be read or written: its key is not available (see the server log)")
	default:
		logger(r.Context()).Error("storage error", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// deletedDocument is current marked deleted: the same fields, the next
// version, and when, by whom and (with a retention) until when it is kept.
func (d *documents) deletedDocument(r *http.Request, c *registry.Collection, current map[string]any) map[string]any {
	now := d.timestamp()
	stored, _ := current["_meta"].(map[string]any)
	meta := map[string]any{"created_at": stored["created_at"], "updated_at": now, "version": version(current) + 1}
	stampUpdate(r, stored, meta)
	meta["deleted_at"] = now
	if caller, ok := callerOf(r); ok {
		meta["deleted_by"] = caller.Subject()
	}
	if c.SoftDelete.Retention > 0 {
		meta["purge_at"] = now.Add(c.SoftDelete.Retention)
	}
	return withMeta(current, meta)
}

// restoredDocument is current without its deletion marks, at the next version.
func (d *documents) restoredDocument(r *http.Request, current map[string]any) map[string]any {
	stored, _ := current["_meta"].(map[string]any)
	meta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": version(current) + 1}
	stampUpdate(r, stored, meta) // keeps owner and created_by; the deletion marks are not copied
	return withMeta(current, meta)
}

// withMeta is the user fields and id of doc with a new _meta.
func withMeta(doc, meta map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	out["_meta"] = meta
	return out
}

// softDelete handles DELETE on a collection that soft-deletes: the document
// stays, marked deleted, and nobody sees it until it is restored or purged.
func (d *documents) softDelete(w http.ResponseWriter, r *http.Request, c *registry.Collection, repo storage.Repository) {
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
		switch err := repo.Replace(r.Context(), d.deletedDocument(r, c, current), read); {
		case err == nil:
			d.auditData(r, auth.AuditDataDelete, c, id)
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

// purge handles DELETE ?purge=true: the document is removed for good, deleted
// or not. It needs the purge rule (and the restore rule to see the trash).
func (d *documents) purge(w http.ResponseWriter, r *http.Request, c *registry.Collection, repo storage.Repository) {
	if c.SoftDelete == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", Detail{Path: "purge", Reason: "this collection doesn't soft-delete (see soft_delete in collection.yaml)"})
		return
	}
	if v := r.URL.Query().Get("purge"); v != "true" {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", Detail{Path: "purge", Reason: "must be true"})
		return
	}
	id := chi.URLParam(r, "id")
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	a := d.accessFor(r)
	filter, ok := viewFilter(w, r, a, c, viewAll)
	if !ok {
		return
	}
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
		if !allowWrite(w, r, a, c, rules.Purge, current, nil) {
			return
		}
		switch err := repo.Delete(r.Context(), id, &read); {
		case err == nil:
			d.documentGone(r.Context(), c, current)
			d.auditData(r, auth.AuditDataPurge, c, id)
			w.WriteHeader(http.StatusNoContent)
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
			logger(r.Context()).Debug("purge conflict, retrying", "id", id, "version", read)
		default:
			storageError(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

// restore handles POST /{id}/restore: a deleted document comes back, as it
// was, at the next version. It has to be valid against the schema as it is
// now, and its unique values must still be free.
func (d *documents) restore(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	if c.SoftDelete == nil {
		notFound(w, r)
		return
	}
	id := chi.URLParam(r, "id")
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	a := d.accessFor(r)
	filter, ok := viewFilter(w, r, a, c, viewTrash)
	if !ok {
		return
	}
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
		if !allowWrite(w, r, a, c, rules.Restore, current, nil) {
			return
		}
		if !validate(w, r, c, timesToStrings(userFields(current)).(map[string]any)) {
			return
		}
		restored := d.restoredDocument(r, current)
		switch err := repo.Replace(r.Context(), restored, read); {
		case err == nil:
			d.auditData(r, auth.AuditDataRestore, c, id)
			w.Header().Set("ETag", etag(version(restored)))
			shown, _ := d.revealerFor(r, c).view(restored)
			writeJSON(w, http.StatusOK, render(shown))
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
			logger(r.Context()).Debug("restore conflict, retrying", "id", id, "version", read)
		default:
			storageError(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

const (
	codeOrgRequired    = "org_required"
	codeTeamRequired   = "team_required"
	codeNotTeamMember  = "not_team_member"
	codeOrgNotAllowed  = "org_not_allowed"
	codeImmutableField = "immutable_field"
)

// createOrg resolves the organization a new document is created in (?org=), checking that the
// caller is a member that counts before any rule runs. It answers and returns false on a
// problem; "" means the document belongs to none.
func (d *documents) createOrg(w http.ResponseWriter, r *http.Request, c *registry.Collection) (string, bool) {
	org := r.URL.Query().Get("org")
	given := r.URL.Query().Has("org")
	if !c.InOrganizations() {
		if given {
			writeError(w, r, http.StatusBadRequest, codeOrgNotAllowed, "this collection has no organizations", Detail{Path: "org", Reason: "this collection doesn't belong to organizations"})
			return "", false
		}
		return "", true
	}
	if org == "" {
		if c.Organization == registry.OrgRequired {
			writeError(w, r, http.StatusBadRequest, codeOrgRequired, "documents of this collection belong to an organization: say which with ?org=", Detail{Path: "org", Reason: "is required"})
			return "", false
		}
		return "", true
	}
	caller, _ := callerOf(r)
	// Administrators (the admin data route) and functions with admin access choose any
	// organization that exists; everyone else is a member of it.
	if adminData(r) || (caller.BypassesRules() && caller.Func != nil) {
		svc := d.users(c.Realm)
		if svc == nil || !svc.OrgActive(r.Context(), org) {
			writeError(w, r, http.StatusNotFound, codeNotFound, "no such organization", Detail{Path: "org", Reason: "no active organization has this id"})
			return "", false
		}
		return org, true
	}
	if caller.User == nil {
		writeError(w, r, http.StatusForbidden, codeNotOrgMember, "you are not a member of that organization")
		return "", false
	}
	if _, member := d.userOrgs(r, c.Realm, caller)[org]; !member {
		writeError(w, r, http.StatusForbidden, codeNotOrgMember, "you are not a member of that organization")
		return "", false
	}
	return org, true
}

// createScope resolves where a new document is created: its organization (?org=) and team
// (?team=), after the caller's membership of each was checked, before any rule runs.
func (d *documents) createScope(w http.ResponseWriter, r *http.Request, c *registry.Collection) (org, team string, ok bool) {
	if org, ok = d.createOrg(w, r, c); !ok {
		return "", "", false
	}
	if team, ok = d.createTeam(w, r, c, org); !ok {
		return "", "", false
	}
	return org, team, true
}

// createTeam resolves the team a new document is created in (?team=, which needs ?org=): the
// caller must belong to it or hold a team role in it. A team the caller isn't part of, an
// unknown one and one of another organization all answer alike.
func (d *documents) createTeam(w http.ResponseWriter, r *http.Request, c *registry.Collection, org string) (string, bool) {
	team := r.URL.Query().Get("team")
	given := r.URL.Query().Has("team")
	if !c.InTeams() {
		if given {
			writeError(w, r, http.StatusBadRequest, codeTeamNotAllowed, "this collection has no teams", Detail{Path: "team", Reason: "documents of this collection don't belong to teams"})
			return "", false
		}
		return "", true
	}
	if team == "" {
		if c.Team == registry.TeamRequired {
			writeError(w, r, http.StatusBadRequest, codeTeamRequired, "documents of this collection belong to a team: say which with ?team=", Detail{Path: "team", Reason: "is required"})
			return "", false
		}
		return "", true
	}
	if org == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "?team= needs ?org=", Detail{Path: "team", Reason: "needs ?org= with the team's organization"})
		return "", false
	}
	caller, _ := callerOf(r)
	if adminData(r) || (caller.BypassesRules() && caller.Func != nil) {
		if svc := d.users(c.Realm); svc == nil || !svc.OrgTeamExists(r.Context(), org, team) {
			writeError(w, r, http.StatusNotFound, codeNotFound, "no such team", Detail{Path: "team", Reason: "the organization has no team with this id"})
			return "", false
		}
		return team, true
	}
	if caller.User != nil {
		if m := d.userOrgs(r, c.Realm, caller)[org]; m != nil && (slices.Contains(m.Teams, team) || len(m.TeamRoles[team]) > 0) {
			return team, true
		}
	}
	writeError(w, r, http.StatusForbidden, codeNotTeamMember, "you are not a member of that team")
	return "", false
}

// scopeFields are the system fields that say where in the organization a document lives, by
// the key they have in `_meta`.
var scopeFields = []struct {
	key  string // in _meta, and the query parameter that names it
	path string // _meta.<key>
	has  func(c *registry.Collection) bool
	// For messages: "organization" "an", the plural and the code of a collection without them.
	noun, article, plural, notAllowed string
}{
	{"org", registry.MetaOrg, (*registry.Collection).InOrganizations, "organization", "an", "organizations", codeOrgNotAllowed},
	{"team", registry.MetaTeam, (*registry.Collection).InTeams, "team", "a", "teams", codeTeamNotAllowed},
}

// scopeMeta is the `_meta` content a create gets from the organization and team it is made in.
func scopeMeta(org, team string) map[string]any {
	out := map[string]any{}
	if org != "" {
		out["org"] = org
	}
	if team != "" {
		out["team"] = team
	}
	return out
}

// claimedScope reads the organization and team a PUT or PATCH body says the document belongs to.
func claimedScope(c *registry.Collection, body map[string]any) map[string]string {
	meta, ok := body["_meta"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, f := range scopeFields {
		if v, has := meta[f.key]; has && f.has(c) {
			s, _ := v.(string)
			out[f.key] = s
		}
	}
	return out
}

// sameScope answers 400 immutable_field when the body names another organization or team than
// the document's: moving a document is an administrator's transfer, not an update.
func sameScope(w http.ResponseWriter, r *http.Request, claimed map[string]string, current map[string]any) bool {
	meta, _ := current["_meta"].(map[string]any)
	for _, f := range scopeFields {
		want, claims := claimed[f.key]
		if have, _ := meta[f.key].(string); claims && want != have {
			writeError(w, r, http.StatusBadRequest, codeImmutableField, f.path+" can't be changed: a document is moved by an administrator", Detail{Path: f.path, Reason: "is set at creation and can't change"})
			return false
		}
	}
	return true
}

// expandPlanOf reads and checks the request's `expand`; plan is nil without one. It answers
// 400 invalid_expand and returns false when the parameter is wrong.
func (d *documents) expandPlanOf(w http.ResponseWriter, r *http.Request, c *registry.Collection) (*expandPlan, bool) {
	if !r.URL.Query().Has("expand") {
		return nil, true
	}
	plan, details := d.planExpand(c, r.URL.Query().Get("expand"))
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidExpand, "invalid expand: "+details[0].Reason, details...)
		return nil, false
	}
	return plan, true
}

// expandInto expands docs into out and sets the cost headers; false when it answered an error.
func (d *documents) expandInto(w http.ResponseWriter, r *http.Request, plan *expandPlan, docs []storage.Document, out []map[string]any) bool {
	start := time.Now()
	queries, count, err := d.expandDocuments(r.Context(), r, plan, docs, out)
	if err != nil {
		expandError(w, r, err)
		return false
	}
	setExpandHeaders(w, r, plan, queries, count, time.Since(start))
	return true
}

// explainReferenceFilters tells a caller who filters or sorts on a reference's fields why
// that isn't possible: where and order_by only see the collection's own fields.
func explainReferenceFilters(c *registry.Collection, details []Detail) []Detail {
	for i, dt := range details {
		// The path of a detail is where.<field> or order_by.<field>.
		_, field, _ := strings.Cut(dt.Path, ".")
		first, _, _ := strings.Cut(field, ".")
		if _, declared := c.Fields[field]; declared {
			continue
		}
		if _, isRef := c.References[first]; isRef || first == "_expanded" {
			details[i].Reason = "expanded documents can't be filtered or sorted on: where and order_by use this collection's own fields (filter on the reference's source field, or see Reference expansion in the docs)"
		}
	}
	return details
}

// scopeFilter adds the ?org= and ?team= shorthands of a read to filter, as conditions on
// _meta.org and _meta.team. It answers 400 and returns false when the collection has no such
// scope or the value is empty.
func scopeFilter(w http.ResponseWriter, r *http.Request, c *registry.Collection, filter storage.Filter) (storage.Filter, bool) {
	for _, f := range scopeFields {
		if !r.URL.Query().Has(f.key) {
			continue
		}
		if !f.has(c) {
			writeError(w, r, http.StatusBadRequest, f.notAllowed, "this collection has no "+f.plural)
			return nil, false
		}
		id := r.URL.Query().Get(f.key)
		if id == "" {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", Detail{Path: f.key, Reason: "must be " + f.article + " " + f.noun + " id"})
			return nil, false
		}
		filter = and(filter, storage.Condition{Field: f.path, Op: storage.OpEq, Value: id})
	}
	return filter, true
}

// maxRelevanceResults is how deep a relevance-sorted search can page: past it the database
// would score and sort ever more documents to show a page nobody reads.
const maxRelevanceResults = 1000

// relevanceSort reports whether order_by is the relevance sort.
func relevanceSort(orderBy string) bool { return strings.TrimSpace(orderBy) == "_relevance" }

// queryCode is the code of a refused query: field_encrypted when it names an encrypted field,
// invalid_query otherwise.
func queryCode(details []Detail) string {
	for _, d := range details {
		if d.Code == codeFieldEncrypted {
			return codeFieldEncrypted
		}
	}
	return codeInvalidQuery
}
