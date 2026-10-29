package client

import (
	"container/list"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// CacheOptions tune the revalidation cache. It is on by default.
type CacheOptions struct {
	// Disable turns the cache off.
	Disable bool
	// MaxEntries bounds the number of reads kept; 200 when zero.
	MaxEntries int
	// MaxBytes bounds the size of the bodies kept; 5 MiB when zero.
	MaxBytes int
}

// CacheStats says how the revalidation cache is doing.
type CacheStats struct {
	// Hits are reads the server answered 304 to: the kept body was current.
	Hits int
	// Misses are cacheable reads that came back in full.
	Misses int
	// Entries and Bytes are what is kept.
	Entries int
	Bytes   int
}

type cacheEntry struct {
	key    string
	etag   string
	data   []byte
	header http.Header
}

// RevalidationCache keeps the last body and ETag of document and list reads and revalidates them
// with If-None-Match, so an unchanged read costs a 304. Entries read as one caller (a session or
// an API key) are never reused for another. It is safe for concurrent use.
type RevalidationCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	entries    map[string]*list.Element // most recently used at the front
	order      *list.List
	bytes      int
	hits       int
	misses     int
	owner      string
}

// NewRevalidationCache returns an empty cache.
func NewRevalidationCache(o CacheOptions) *RevalidationCache {
	c := &RevalidationCache{maxEntries: o.MaxEntries, maxBytes: o.MaxBytes, entries: map[string]*list.Element{}, order: list.New()}
	if c.maxEntries <= 0 {
		c.maxEntries = 200
	}
	if c.maxBytes <= 0 {
		c.maxBytes = 5 << 20
	}
	return c
}

// scope forgets everything when the caller changes.
func (c *RevalidationCache) scope(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner != c.owner {
		c.clearLocked()
		c.owner = owner
	}
}

func (c *RevalidationCache) get(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.entries[key]
	if el == nil {
		return nil
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheEntry)
}

func (c *RevalidationCache) set(e *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteLocked(e.key)
	// A body that alone would exceed the bound is not worth keeping.
	if len(e.data) > c.maxBytes {
		return
	}
	c.entries[e.key] = c.order.PushFront(e)
	c.bytes += len(e.data)
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.deleteLocked(oldest.Value.(*cacheEntry).key)
	}
}

func (c *RevalidationCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteLocked(key)
}

func (c *RevalidationCache) deleteLocked(key string) {
	if el := c.entries[key]; el != nil {
		c.bytes -= len(el.Value.(*cacheEntry).data)
		c.order.Remove(el)
		delete(c.entries, key)
	}
}

// dropPath forgets every entry read from a path, whatever its query: a write changed it.
func (c *RevalidationCache) dropPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if u, err := url.Parse(key); err == nil && u.Path == path {
			c.deleteLocked(key)
		}
	}
}

// Clear forgets everything.
func (c *RevalidationCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearLocked()
}

func (c *RevalidationCache) clearLocked() {
	c.entries = map[string]*list.Element{}
	c.order.Init()
	c.bytes = 0
}

func (c *RevalidationCache) hit()  { c.mu.Lock(); c.hits++; c.mu.Unlock() }
func (c *RevalidationCache) miss() { c.mu.Lock(); c.misses++; c.mu.Unlock() }

// Stats says how the cache is doing.
func (c *RevalidationCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Hits: c.hits, Misses: c.misses, Entries: c.order.Len(), Bytes: c.bytes}
}

// CacheStats says how the client's revalidation cache is doing; zero when it is off.
func (c *Client) CacheStats() CacheStats {
	if c.cache == nil {
		return CacheStats{}
	}
	return c.cache.Stats()
}

// ClearCache forgets everything the revalidation cache holds.
func (c *Client) ClearCache() {
	if c.cache != nil {
		c.cache.Clear()
	}
}

// cacheable says whether a call is a read of a document or a list the cache may keep: a GET of
// /db/collection or /db/collection/id, without file links or expansions (which change with time or
// with other documents).
func cacheable(r *request) bool {
	if r.method != http.MethodGet || r.wantRaw || len(r.path) < 2 || len(r.path) > 3 {
		return false
	}
	for _, s := range r.path {
		if strings.HasPrefix(s, "_") {
			return false
		}
	}
	return r.query.Get("file_links") == "" && r.query.Get("expand") == ""
}

// cacheCtx is the cache as one call sees it.
type cacheCtx struct {
	cache *RevalidationCache
	read  bool // this call is a cacheable read
	owner string
}

func (c *Client) cacheFor(r *request, h http.Header, owner string) *cacheCtx {
	return &cacheCtx{cache: c.cache, read: c.cache != nil && !r.noCache && cacheable(r), owner: owner}
}

// lookup sends the tag of the copy held, if any. A caller's own If-None-Match is theirs to handle.
func (cc *cacheCtx) lookup(key string, h http.Header) *cacheEntry {
	if !cc.read {
		return nil
	}
	cc.cache.scope(cc.owner)
	if h.Get("If-None-Match") != "" {
		return nil
	}
	held := cc.cache.get(key)
	if held != nil {
		h.Set("If-None-Match", held.etag)
	}
	return held
}

// answer handles the answers the cache has a say in. done is true when the result is final.
func (cc *cacheCtx) answer(res *http.Response, key string, held *cacheEntry, r *request) (done bool, out *Response, err error) {
	switch {
	case res.StatusCode == http.StatusNotModified:
		res.Body.Close()
		if cc.read && held != nil {
			cc.cache.hit()
			// The copy held is current; its headers (ETag, …) are the ones the 304 repeats.
			return true, &Response{Status: 200, Header: held.header, Data: held.data, FromCache: true}, nil
		}
		// The caller asked with their own If-None-Match: the 304 is their answer.
		return true, &Response{Status: 304, Header: res.Header}, nil
	case res.StatusCode >= 200 && res.StatusCode < 300 && cc.cache != nil && writesDocument(r):
		// A write changed it: what is held is stale.
		if u, err := url.Parse(key); err == nil {
			cc.cache.dropPath(u.Path)
		}
	}
	return false, nil, nil
}

var noStore = regexp.MustCompile(`(?i)no-store`)

// store keeps the body of a successful cacheable read with its tag.
func (cc *cacheCtx) store(key string, res *http.Response, data []byte) {
	if !cc.read {
		return
	}
	cc.cache.miss()
	etag := res.Header.Get("ETag")
	if etag != "" && !noStore.MatchString(res.Header.Get("Cache-Control")) && len(data) > 0 {
		cc.cache.set(&cacheEntry{key: key, etag: etag, data: data, header: res.Header.Clone()})
		return
	}
	cc.cache.delete(key)
}

// writesDocument says whether a call writes /db/collection/id.
func writesDocument(r *request) bool {
	return r.method != http.MethodGet && r.method != http.MethodHead && len(r.path) == 3 &&
		!strings.HasPrefix(r.path[0], "_") && !strings.HasPrefix(r.path[1], "_")
}
