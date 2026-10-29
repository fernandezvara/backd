package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"strings"
)

// Meta is what backd keeps next to every document, in its _meta.
type Meta struct {
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Version increases on every write; use it with [WithIfMatch].
	Version int64 `json:"version,omitempty"`
	// Owner is the id of the user who created it (realms with auth enabled).
	Owner     string `json:"owner,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
	// Org is the organization the document belongs to (collections with organization).
	Org string `json:"org,omitempty"`
	// Team is the team of the organization the document belongs to (collections with team).
	Team string `json:"team,omitempty"`
	// Score is the relevance to a $search, in a list sorted by "_relevance".
	Score     *float64 `json:"score,omitempty"`
	DeletedAt string   `json:"deleted_at,omitempty"`
	DeletedBy string   `json:"deleted_by,omitempty"`
	// PurgeAt is when a soft-deleted document is removed for good.
	PurgeAt string `json:"purge_at,omitempty"`
	// Redacted lists the encrypted fields the caller may not see: they come back null, and a
	// null here is hidden, not empty.
	Redacted []string `json:"redacted,omitempty"`
}

// Base is what every document has. Embed it in the struct of a document:
//
//	type Post struct {
//	    client.Base
//	    Title string `json:"title"`
//	}
type Base struct {
	ID       string         `json:"id"`
	Meta     Meta           `json:"_meta"`
	Expanded map[string]any `json:"_expanded,omitempty"`
}

// Doc is a document read without a type of its own.
type Doc map[string]any

// ID returns the document's id.
func (d Doc) ID() string { s, _ := d["id"].(string); return s }

// Meta returns the document's _meta.
func (d Doc) Meta() Meta {
	var m Meta
	if raw, ok := d["_meta"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// Page is one page of a list.
type Page[T any] struct {
	Items   []T  `json:"items"`
	Limit   int  `json:"limit"`
	Skip    int  `json:"skip"`
	HasMore bool `json:"has_more"`
	// NextCursor, with HasMore, is the After of the next page.
	NextCursor string `json:"next_cursor,omitempty"`
	// Total is set when the query asked to Count; it counts the whole list, not what follows After.
	Total *int `json:"total,omitempty"`
}

// Database is a database of the realm: client.DB("main").Collection("posts").
type Database struct {
	c    *Client
	name string
}

// DB returns a database of the realm.
func (c *Client) DB(name string) *Database { return &Database{c: c, name: name} }

// Name returns the database's name.
func (d *Database) Name() string { return d.name }

// Collection returns a collection of the database.
func (d *Database) Collection(name string) *Collection {
	return &Collection{c: d.c, path: []string{d.name, name}}
}

// BatchOperation is one operation of a batch.
type BatchOperation struct {
	Op         string         `json:"op"` // "create", "replace", "patch" or "delete"
	Collection string         `json:"collection"`
	ID         string         `json:"id,omitempty"`
	Document   map[string]any `json:"document,omitempty"`
	Patch      map[string]any `json:"patch,omitempty"`
	// IfMatch is the version the document must have: an int, or an ETag string.
	IfMatch any `json:"-"`
}

// Batch runs operations on several collections of the database, all or none. The results come in
// the order of the operations: the written document for create, replace and patch, and just its
// id for delete.
func (d *Database) Batch(ctx context.Context, ops []BatchOperation, opts ...RequestOption) ([]Doc, error) {
	wire := make([]map[string]any, len(ops))
	for i, op := range ops {
		b, _ := json.Marshal(op)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		if op.IfMatch != nil {
			v, err := IfMatchValue(op.IfMatch)
			if err != nil {
				return nil, err
			}
			m["if_match"] = v
		}
		wire[i] = m
	}
	var out struct {
		Results []Doc `json:"results"`
	}
	if _, err := d.c.Do(ctx, "POST", []string{d.name, "_batch"}, map[string]any{"operations": wire}, &out, opts...); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// IfMatchValue is the If-Match of a version: a number becomes the quoted ETag the server compares.
func IfMatchValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int:
		return `"` + strconv.Itoa(x) + `"`, nil
	case int64:
		return `"` + strconv.FormatInt(x, 10) + `"`, nil
	case uint64:
		return `"` + strconv.FormatUint(x, 10) + `"`, nil
	}
	return "", fmt.Errorf("client: If-Match takes a version number or an ETag string, not %T", v)
}

// WithIfMatch makes a write conditional on the document's version (its _meta.version, or an ETag).
// A document that has moved on is an error matching ErrVersionMismatch. A conditional write is
// safe to retry, so the client's retry settings apply to it.
func WithIfMatch(version any) RequestOption {
	return func(r *request) {
		v, err := IfMatchValue(version)
		if err != nil {
			r.err = err
			return
		}
		r.headers.Set("If-Match", v)
	}
}

// WithIdempotencyKey makes a create safe to repeat: the same key returns the first answer. It
// applies to creates, batches and function calls.
func WithIdempotencyKey(key string) RequestOption {
	return func(r *request) { r.headers.Set("Idempotency-Key", key) }
}

// WithTeam scopes a call to a team of the organization (collections with team).
func WithTeam(team string) RequestOption { return WithQuery(url.Values{"team": {team}}) }

// WithOrg scopes a call to an organization (collections with organization), by id or slug.
func WithOrg(org string) RequestOption { return WithQuery(url.Values{"org": {org}}) }

// WithPurge makes a delete remove the document for good instead of soft-deleting it.
func WithPurge() RequestOption { return WithQuery(url.Values{"purge": {"true"}}) }

// ReadOptions are the options of a read of one document.
type ReadOptions struct {
	// Deleted is "only" or "include" to read a soft-deleted document.
	Deleted   string
	FileLinks bool
	Expand    string
}

// Collection is a collection of a database. Its calls return documents as [Doc]; use [Typed] to
// get structs.
type Collection struct {
	c    *Client
	path []string
	team string
}

// Name returns the collection's name.
func (c *Collection) Name() string { return c.path[1] }

// Path returns the database and collection names.
func (c *Collection) Path() []string { return append([]string{}, c.path...) }

// Client returns the client the collection reads through.
func (c *Collection) Client() *Client { return c.c }

// InTeam returns the collection scoped to a team of the organization: lists and creates reach
// that team's documents.
func (c *Collection) InTeam(team string) *Collection {
	return &Collection{c: c.c, path: c.path, team: team}
}

func (c *Collection) scope() url.Values {
	if c.team == "" {
		return nil
	}
	return url.Values{"team": {c.team}}
}

// Typed decodes the documents of a collection into T, a struct that embeds [Base]. The calls are
// those of [Collection].
type Typed[T any] struct{ coll *Collection }

// As returns the collection reading and writing documents as T.
func As[T any](c *Collection) *Typed[T] { return &Typed[T]{coll: c} }

// Collection returns the untyped collection.
func (t *Typed[T]) Collection() *Collection { return t.coll }

// List reads a page of documents.
func (t *Typed[T]) List(ctx context.Context, q Query, opts ...RequestOption) (*Page[T], error) {
	v, err := q.values()
	if err != nil {
		return nil, err
	}
	var page Page[T]
	opts = append([]RequestOption{WithQuery(v), WithQuery(t.coll.scope())}, opts...)
	if _, err := t.coll.c.Do(ctx, "GET", t.coll.path, nil, &page, opts...); err != nil {
		return nil, err
	}
	return &page, nil
}

// Search lists the documents that match the text, best first. The text is words, "quoted
// phrases" and -excluded words. q.Where is combined with the search; q.OrderBy and q.After are
// not used (relevance pages by Skip).
func (t *Typed[T]) Search(ctx context.Context, text string, q Query, opts ...RequestOption) (*Page[T], error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("client: search needs the text to search for")
	}
	base := Where{}
	switch w := q.Where.(type) {
	case nil:
	case string:
		if err := json.Unmarshal([]byte(w), &base); err != nil {
			return nil, fmt.Errorf("client: the where is not a JSON object: %w", err)
		}
	default:
		b, err := json.Marshal(w)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &base); err != nil {
			return nil, fmt.Errorf("client: the where is not a JSON object: %w", err)
		}
	}
	base["$search"] = text
	q.Where, q.OrderBy, q.After = base, []string{"_relevance"}, ""
	return t.List(ctx, q, opts...)
}

// Iterate reads every document the query matches, page after page, following the cursors. The
// loop ends at the first error, which is the second value of the last iteration.
func (t *Typed[T]) Iterate(ctx context.Context, q Query, opts ...RequestOption) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		limit := q.Limit
		if limit == 0 {
			limit = 100
		}
		q.Limit = limit
		for {
			page, err := t.List(ctx, q, opts...)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Items {
				if !yield(item, nil) {
					return
				}
			}
			if !page.HasMore {
				return
			}
			switch {
			case page.NextCursor != "":
				q.After, q.Skip = page.NextCursor, 0
			case q.After != "":
				var zero T
				yield(zero, errors.New("client: the list has no next_cursor to continue after"))
				return
			default:
				q.Skip += limit
			}
		}
	}
}

// Get reads one document.
func (t *Typed[T]) Get(ctx context.Context, id string, o ReadOptions, opts ...RequestOption) (*T, error) {
	q := url.Values{}
	if o.Deleted != "" {
		q.Set("deleted", o.Deleted)
	}
	if o.FileLinks {
		q.Set("file_links", "true")
	}
	if o.Expand != "" {
		q.Set("expand", o.Expand)
	}
	var doc T
	if _, err := t.coll.c.Do(ctx, "GET", append(t.coll.Path(), id), nil, &doc, append([]RequestOption{WithQuery(q)}, opts...)...); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Create writes a new document and returns it as stored. Use [WithIdempotencyKey] to make a
// repeat harmless, [WithOrg] to say the organization.
func (t *Typed[T]) Create(ctx context.Context, doc any, opts ...RequestOption) (*T, error) {
	opts = append([]RequestOption{WithQuery(t.coll.scope())}, opts...)
	var out T
	if _, err := t.coll.c.Do(ctx, "POST", t.coll.path, doc, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Replace replaces a document; use [WithIfMatch] to not overwrite a newer version.
func (t *Typed[T]) Replace(ctx context.Context, id string, doc any, opts ...RequestOption) (*T, error) {
	var out T
	if _, err := t.coll.c.Do(ctx, "PUT", append(t.coll.Path(), id), doc, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Patch merges changes into a document (JSON Merge Patch: a null removes a field); use
// [WithIfMatch] to not overwrite a newer version.
func (t *Typed[T]) Patch(ctx context.Context, id string, patch any, opts ...RequestOption) (*T, error) {
	var out T
	opts = append([]RequestOption{WithContentType("application/merge-patch+json")}, opts...)
	if _, err := t.coll.c.Do(ctx, "PATCH", append(t.coll.Path(), id), patch, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deletes a document; in a collection with soft_delete it can be restored, unless
// [WithPurge] removes it for good.
func (t *Typed[T]) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := t.coll.c.Do(ctx, "DELETE", append(t.coll.Path(), id), nil, nil, opts...)
	return err
}

// Restore brings back a soft-deleted document.
func (t *Typed[T]) Restore(ctx context.Context, id string, opts ...RequestOption) (*T, error) {
	var out T
	if _, err := t.coll.c.Do(ctx, "POST", append(t.coll.Path(), id, "restore"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// The calls of Collection are those of Typed[Doc].
func (c *Collection) docs() *Typed[Doc] { return &Typed[Doc]{coll: c} }

// List reads a page of documents.
func (c *Collection) List(ctx context.Context, q Query, opts ...RequestOption) (*Page[Doc], error) {
	return c.docs().List(ctx, q, opts...)
}

// Search lists the documents that match the text, best first.
func (c *Collection) Search(ctx context.Context, text string, q Query, opts ...RequestOption) (*Page[Doc], error) {
	return c.docs().Search(ctx, text, q, opts...)
}

// Iterate reads every document the query matches, following the cursors.
func (c *Collection) Iterate(ctx context.Context, q Query, opts ...RequestOption) iter.Seq2[Doc, error] {
	return c.docs().Iterate(ctx, q, opts...)
}

// Get reads one document.
func (c *Collection) Get(ctx context.Context, id string, o ReadOptions, opts ...RequestOption) (Doc, error) {
	d, err := c.docs().Get(ctx, id, o, opts...)
	if err != nil {
		return nil, err
	}
	return *d, nil
}

// Create writes a new document and returns it as stored.
func (c *Collection) Create(ctx context.Context, doc any, opts ...RequestOption) (Doc, error) {
	d, err := c.docs().Create(ctx, doc, opts...)
	if err != nil {
		return nil, err
	}
	return *d, nil
}

// Replace replaces a document.
func (c *Collection) Replace(ctx context.Context, id string, doc any, opts ...RequestOption) (Doc, error) {
	d, err := c.docs().Replace(ctx, id, doc, opts...)
	if err != nil {
		return nil, err
	}
	return *d, nil
}

// Patch merges changes into a document.
func (c *Collection) Patch(ctx context.Context, id string, patch any, opts ...RequestOption) (Doc, error) {
	d, err := c.docs().Patch(ctx, id, patch, opts...)
	if err != nil {
		return nil, err
	}
	return *d, nil
}

// Delete deletes a document.
func (c *Collection) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return c.docs().Delete(ctx, id, opts...)
}

// Restore brings back a soft-deleted document.
func (c *Collection) Restore(ctx context.Context, id string, opts ...RequestOption) (Doc, error) {
	d, err := c.docs().Restore(ctx, id, opts...)
	if err != nil {
		return nil, err
	}
	return *d, nil
}

// AggregateParams are the parameters of an aggregation.
type AggregateParams struct {
	// Where is the same filter as a list's, with $search; the read rules apply first.
	Where any
	// GroupBy lists up to 3 declared fields; a date takes a bucket: "paid_at:month".
	GroupBy []string
	// Metrics are "count" (the default), "count_distinct:<field>", "sum:<field>", "avg:<field>", …
	Metrics []string
	// TZ is the IANA zone of the date buckets ("Europe/Madrid"); UTC by default.
	TZ string
	// OrderBy lists group keys and result names, "-" for descending: "-count".
	OrderBy []string
	// Limit is the number of groups returned.
	Limit int
	// Language is the language of a $search in Where.
	Language string
}

// AggregateGroup is one group of an aggregation: its key and the values of the metrics.
type AggregateGroup struct {
	// Key maps each grouped field (with its bucket, "paid_at:month") to the group's value.
	Key map[string]any
	// Values maps each metric ("count", "sum_total", …) to its value.
	Values map[string]any
}

// Count returns the group's count metric.
func (g AggregateGroup) Count() (float64, bool) { f, ok := g.Values["count"].(float64); return f, ok }

func (g *AggregateGroup) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	g.Key, g.Values = map[string]any{}, map[string]any{}
	for k, raw := range m {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		if k == "key" {
			if km, ok := v.(map[string]any); ok {
				g.Key = km
			}
			continue
		}
		g.Values[k] = v
	}
	return nil
}

func (g AggregateGroup) MarshalJSON() ([]byte, error) {
	m := map[string]any{"key": g.Key}
	for k, v := range g.Values {
		m[k] = v
	}
	return json.Marshal(m)
}

// AggregateResult is the answer of an aggregation.
type AggregateResult struct {
	Groups []AggregateGroup `json:"groups"`
	// TotalGroups is how many groups there were before Limit.
	TotalGroups int `json:"total_groups"`
}

// Aggregate groups the documents and computes metrics over them, on the server.
func (c *Collection) Aggregate(ctx context.Context, p AggregateParams, opts ...RequestOption) (*AggregateResult, error) {
	w, err := whereJSON(p.Where)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("where", w)
	set("group_by", strings.Join(p.GroupBy, ","))
	set("metrics", strings.Join(p.Metrics, ","))
	set("tz", p.TZ)
	set("order_by", strings.Join(p.OrderBy, ","))
	if p.Limit != 0 {
		set("limit", strconv.Itoa(p.Limit))
	}
	set("language", p.Language)
	var out AggregateResult
	opts = append([]RequestOption{WithQuery(v), WithQuery(c.scope())}, opts...)
	if _, err := c.c.Do(ctx, "GET", append(c.Path(), "_aggregate"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}
