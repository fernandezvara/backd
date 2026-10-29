/**
 * Revalidation cache: the last body and `ETag` of each document and list read,
 * kept in memory so the next read can send `If-None-Match` and take a `304`.
 * Nothing here is served without asking the server: every read still goes
 * out, and the server (which runs the rules) decides.
 */

/**
 * @typedef {object} CacheOptions
 * @property {number} [maxEntries]  Most entries kept; default 200.
 * @property {number} [maxBytes]    Most bytes of bodies kept; default 5 MiB.
 */

/**
 * What `client.cacheStats()` reports.
 * @typedef {object} CacheStats
 * @property {number} hits     Reads the server answered `304` to: the cached body was used.
 * @property {number} misses   Cacheable reads that came back in full.
 * @property {number} entries
 * @property {number} bytes
 */

/**
 * @typedef {object} Entry
 * @property {string} etag
 * @property {string} text      The body, as received.
 * @property {Headers} headers
 */

export class RevalidationCache {
  /** @param {CacheOptions} [options] */
  constructor(options = {}) {
    this.maxEntries = options.maxEntries ?? 200
    this.maxBytes = options.maxBytes ?? 5 * 1024 * 1024
    /** @type {Map<string, Entry>} Insertion order is recency: the oldest is first. */
    this.entries = new Map()
    this.bytes = 0
    this.hits = 0
    this.misses = 0
    /** Who the entries belong to (see `scope`); a different one empties the cache. */
    this.owner = ''
  }

  /**
   * Empties the cache when the caller isn't the one its entries were read as.
   * @param {string} owner  Identifies the credentials of the request, never sent anywhere.
   */
  scope(owner) {
    if (owner !== this.owner) {
      this.clear()
      this.owner = owner
    }
  }

  /** @param {string} key */
  get(key) {
    const e = this.entries.get(key)
    if (e) {
      this.entries.delete(key)
      this.entries.set(key, e) // most recently used
    }
    return e
  }

  /**
   * @param {string} key
   * @param {Entry} entry
   */
  set(key, entry) {
    this.delete(key)
    // A body that alone would exceed the bound is not worth keeping.
    if (entry.text.length > this.maxBytes) return
    this.entries.set(key, entry)
    this.bytes += entry.text.length
    while (this.entries.size > this.maxEntries || this.bytes > this.maxBytes) {
      const oldest = this.entries.keys().next().value
      if (oldest === undefined) break
      this.delete(oldest)
    }
  }

  /** @param {string} key */
  delete(key) {
    const e = this.entries.get(key)
    if (e) {
      this.bytes -= e.text.length
      this.entries.delete(key)
    }
  }

  /**
   * Drops every entry of a path, whatever its query: a document after a write to it.
   * @param {string} pathname
   */
  dropPath(pathname) {
    for (const key of [...this.entries.keys()]) {
      if (new URL(key).pathname === pathname) this.delete(key)
    }
  }

  clear() {
    this.entries.clear()
    this.bytes = 0
  }

  /** @returns {import('./cache.js').CacheStats} */
  stats() {
    return { hits: this.hits, misses: this.misses, entries: this.entries.size, bytes: this.bytes }
  }
}

/**
 * Whether a request is a document or list read the cache may keep:
 * `[database, collection]` or `[database, collection, id]`, without the
 * parameters that make an answer per-response (signed file links).
 * @param {{ method: string, path: string[], query?: Record<string, unknown>, raw?: boolean, rawBody?: unknown }} req
 */
export function cacheable(req) {
  const { path } = req
  if (req.method !== 'GET' || req.raw || path.length < 2 || path.length > 3) return false
  if (path.some((s) => s.startsWith('_'))) return false
  const q = req.query ?? {}
  return q.file_links === undefined && q.expand === undefined
}
