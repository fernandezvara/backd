import { Sha256 } from './sha256.js'
import { COOKIE_SESSION } from './storage.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * Where the configuration is kept between page loads: the part of `localStorage` the client uses.
 * @typedef {object} ConfigStorage
 * @property {(key: string) => string | null} getItem
 * @property {(key: string, value: string) => void} setItem
 * @property {(key: string) => void} removeItem
 */

/**
 * One cached answer: the values of the paths asked for, with what revalidates them.
 * @typedef {object} ConfigEntry
 * @property {string} etag
 * @property {Record<string, any>} values   Nested, as the server returned them.
 * @property {string} version
 * @property {number} fetchedAt             Milliseconds since the epoch.
 * @property {number} ttl                   Seconds the copy is fresh for.
 */

const PREFIX = 'backd.config:'

/** @param {unknown} v */
const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v)

/**
 * The leaves of a nested document by dot path.
 * @param {Record<string, any>} values
 * @param {string} [prefix]
 * @param {Record<string, any>} [out]
 * @returns {Record<string, any>}
 */
export function flatten(values, prefix = '', out = {}) {
  for (const [k, v] of Object.entries(values ?? {})) {
    const p = prefix ? `${prefix}.${k}` : k
    if (isObject(v)) flatten(v, p, out)
    else out[p] = v
  }
  return out
}

/**
 * @param {Record<string, any>} values
 * @param {string} path
 * @returns {any}
 */
function at(values, path) {
  /** @type {any} */
  let cur = values
  for (const k of path.split('.')) {
    if (!isObject(cur) || !(k in cur)) return undefined
    cur = cur[k]
  }
  return cur
}

/** @param {string} text */
async function digest(text) {
  const h = new Sha256()
  h.update(new TextEncoder().encode(text))
  return h.hex().slice(0, 16)
}

/**
 * The realm's app configuration: `client.config`. Values come from `GET /v1/{realm}/_config`, which
 * answers only what the signed-in user's rules allow, with defaults filled in.
 *
 * Copies are kept per realm and per user (in `localStorage` in browsers, in memory elsewhere):
 * fresh for the `cache_ttl` the server gave, with no request; after that reads return the copy at
 * once and revalidate it in the background with `If-None-Match`, which usually costs a `304`.
 * They are dropped on sign-out and when another user signs in.
 *
 * ```js
 * await client.config.load(['features', 'maintenance'])
 * client.config.get('maintenance.enabled') // from the copy; fetched first if never loaded
 * client.config.on('change', (paths) => render(paths))
 * ```
 */
export class Config {
  /**
   * @param {Client} client
   * @param {ConfigStorage | false} [storage]  Where copies persist; `localStorage` in a browser by default, `false` for memory only.
   */
  constructor(client, storage) {
    /** @internal */
    this.client = client
    /** @internal @type {ConfigStorage | undefined} */
    this.store = storage === false ? undefined : storage ?? defaultStorage()
    /** @internal @type {Map<string, ConfigEntry>} Entries by the paths they were asked with ('' is everything). */
    this.entries = new Map()
    /** @internal The caller the entries belong to. */
    this.owner = ''
    /** @internal @type {Map<string, Promise<ConfigEntry>>} */
    this.inflight = new Map()
    /** @internal @type {Set<(paths: string[]) => void>} */
    this.listeners = new Set()
    /** @internal @type {Set<(err: unknown) => void>} */
    this.errorListeners = new Set()
    /** @internal @type {(() => void) | undefined} */
    this.stopLive = undefined
  }

  /**
   * Follows the configuration live: adds a `_config` subscription to the client's realtime stream, and when
   * backd says something the user reads changed, reads those branches again. The `change` listeners hear of
   * the keys that differ, within the realtime delay instead of `cache_ttl`. The stream is the one
   * `client.realtime` keeps (it needs realtime enabled in the deployment). Calling it again replaces the
   * previous call. Errors (the realm has no configuration, realtime is off) go to `on('error')`.
   *
   * ```js
   * await client.config.load(['maintenance'])
   * const stop = client.config.live(['maintenance'])
   * client.config.on('change', (paths) => rerender(paths))
   * ```
   * @param {string[] | string} [paths]  Branches to follow; everything readable by default.
   * @returns {() => void} Stops following.
   */
  live(paths) {
    this.stopLive?.()
    const list = normalize(paths).split(',').filter(Boolean)
    const stop = this.client.realtime.followConfig({
      paths: list,
      // Whenever the stream opens, what happened before it did was not heard: a conditional read of what is
      // held closes the gap, and costs a 304 when nothing moved.
      ready: () => void this.reread([]),
      changed: (changed) => void this.reread(changed),
      error: (err) => {
        if (this.stopLive === stopOnce) this.stopLive = undefined
        this.emitError(err)
      },
    })
    const stopOnce = () => {
      if (this.stopLive === stopOnce) this.stopLive = undefined
      stop()
    }
    this.stopLive = stopOnce
    return stopOnce
  }

  /**
   * The credentials of the client changed: a followed stream is opened again with the new ones, so it
   * reports what the new user reads.
   * @internal
   */
  credentialChanged() {
    if (this.stopLive) this.client.realtime.schedule()
  }

  /**
   * Reads again what is held of the branches that changed (all of it for none), waiting for a read that
   * was already out, which may have started before the change.
   * @internal
   * @param {string[]} changed
   */
  async reread(changed) {
    await this.scope()
    for (const key of [...this.entries.keys()]) {
      if (changed.length > 0 && !touches(key, changed)) continue
      await this.inflight.get(key)?.catch(() => {})
      await this.revalidate(key)
    }
  }

  /**
   * Who the copies belong to: the credentials of the client, hashed (nothing secret is stored).
   * @internal
   */
  async scope() {
    let credential = ''
    if (this.client.apiKey) credential = 'key:' + this.client.apiKey
    else {
      const token = await this.client.storage.get()
      if (token) credential = token === COOKIE_SESSION ? 'cookie' : 'session:' + token
    }
    const owner = credential ? await digest(credential) : 'anonymous'
    if (owner !== this.owner) {
      this.entries.clear()
      this.owner = owner
      this.restore()
    }
    return owner
  }

  /** @internal */
  storageKey() {
    return `${PREFIX}${this.client.realm}:${this.owner}`
  }

  /** @internal */
  restore() {
    if (!this.store) return
    try {
      const raw = this.store.getItem(this.storageKey())
      if (!raw) return
      const saved = JSON.parse(raw)
      for (const [k, e] of Object.entries(saved ?? {})) {
        if (e && typeof e.etag === 'string' && isObject(e.values)) this.entries.set(k, /** @type {ConfigEntry} */ (e))
      }
    } catch {
      // blocked or corrupt storage: start from nothing
    }
  }

  /** @internal */
  persist() {
    if (!this.store) return
    try {
      this.store.setItem(this.storageKey(), JSON.stringify(Object.fromEntries(this.entries)))
    } catch {
      // private browsing, a full quota: the memory copy still works
    }
  }

  /**
   * Forgets every copy of this realm, for every user: on sign-out and when another user signs in.
   * @internal
   */
  clear() {
    this.entries.clear()
    this.inflight.clear()
    this.owner = ''
    if (!this.store) return
    try {
      const ls = /** @type {any} */ (this.store)
      const keys = []
      for (let i = 0; i < (ls.length ?? 0); i++) keys.push(ls.key(i))
      for (const k of keys) if (typeof k === 'string' && k.startsWith(`${PREFIX}${this.client.realm}:`)) this.store.removeItem(k)
    } catch {
      // nothing to forget
    }
  }

  /**
   * Fetches the configuration (or the groups and keys named) and keeps it. Without a request while
   * the copy is fresh; with `refresh: true` always asks the server.
   * @param {string[] | string} [paths]  Groups or keys, in dot notation; everything readable by default.
   * @param {RequestOptions & { refresh?: boolean }} [opts]
   * @returns {Promise<Record<string, any>>}  The values, nested.
   */
  async load(paths, opts = {}) {
    const key = normalize(paths)
    await this.scope()
    const entry = this.entries.get(key)
    if (entry && !opts.refresh && this.fresh(entry)) return this.view(key)
    await this.fetch(key, opts)
    return this.view(key)
  }

  /**
   * One value. From the copy held, at once (a stale copy is revalidated in the background); a key whose
   * group was never loaded is fetched first, and only then does this return a promise. `refresh: true` always
   * asks the server and returns a promise. A key the user can't read, or that doesn't exist, is `undefined`.
   * @param {string} path  Dot notation, such as `features.export.enabled`.
   * @param {RequestOptions & { refresh?: boolean }} [opts]
   * @returns {any}
   */
  get(path, opts = {}) {
    const held = this.cover(path)
    if (!held || opts.refresh) {
      return this.scope().then(async () => {
        const known = this.cover(path)
        if (known && !opts.refresh) return at(known.entry.values, path) // loaded while the scope was read
        // A refresh asks again for the copy that holds the key; a key never loaded, for its group.
        await this.fetch(known?.key ?? path.split('.')[0], opts)
        return at(this.cover(path)?.entry.values ?? {}, path)
      })
    }
    if (!this.fresh(held.entry)) void this.revalidate(held.key)
    return at(held.entry.values, path)
  }

  /**
   * One value from the copy held, with no request at all: `fallback` when it was never loaded.
   * @param {string} path
   * @param {any} [fallback]
   * @returns {any}
   */
  getSync(path, fallback) {
    const held = this.cover(path)
    if (!held) return fallback
    const v = at(held.entry.values, path)
    return v === undefined ? fallback : v
  }

  /**
   * `change`: calls the listener with the dot paths whose value changed when a read brings a different
   * configuration. `error`: with the error that ended {@link Config.live}.
   * @param {'change' | 'error'} event
   * @param {(arg: any) => void} listener
   * @returns {() => void} Stops listening.
   */
  on(event, listener) {
    if (event !== 'change' && event !== 'error') throw new TypeError("client.config.on: the events are 'change' and 'error'")
    const set = event === 'change' ? this.listeners : this.errorListeners
    set.add(listener)
    return () => set.delete(listener)
  }

  /** @internal @param {ConfigEntry} e */
  fresh(e) {
    return Date.now() < e.fetchedAt + e.ttl * 1000
  }

  /**
   * The entry that holds a path: the whole configuration, or a group that contains it.
   * @internal
   * @param {string} path
   * @returns {{ key: string, entry: ConfigEntry } | undefined}
   */
  cover(path) {
    const all = this.entries.get('')
    if (all) return { key: '', entry: all }
    for (const [key, entry] of this.entries) {
      for (const p of key.split(',')) {
        if (p && (path === p || path.startsWith(p + '.'))) return { key, entry }
      }
    }
    return undefined
  }

  /** @internal @param {string} key */
  view(key) {
    const e = this.entries.get(key)
    return e ? structuredClone(e.values) : {}
  }

  /**
   * Asks the server (with the tag of the copy held) and keeps the answer.
   * @internal
   * @param {string} key
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConfigEntry>}
   */
  fetch(key, opts = {}) {
    const running = this.inflight.get(key)
    if (running) return running
    const p = this.doFetch(key, opts).finally(() => this.inflight.delete(key))
    this.inflight.set(key, p)
    return p
  }

  /** @internal @param {string} key @param {RequestOptions} opts @returns {Promise<ConfigEntry>} */
  async doFetch(key, opts) {
    const owner = this.owner
    const held = this.entries.get(key)
    const res = await this.client.request({
      method: 'GET',
      path: ['_config'],
      query: key ? { paths: key } : undefined,
      headers: { ...(held ? { 'If-None-Match': held.etag } : {}), ...opts.headers },
      signal: opts.signal,
      retry: opts.retry,
      cache: false,
    })
    if (owner !== this.owner) {
      // The user changed while the request was out: what came back is not theirs to keep.
      return held ?? { etag: '', values: {}, version: '', fetchedAt: 0, ttl: 0 }
    }
    if (res.status === 304 && held) {
      held.fetchedAt = Date.now()
      held.ttl = ttlOf(res.headers, held.ttl)
      this.persist()
      return held
    }
    const body = res.data ?? {}
    const entry = {
      etag: res.headers.get('ETag') ?? '',
      values: isObject(body.values) ? body.values : {},
      version: String(body.version ?? ''),
      fetchedAt: Date.now(),
      ttl: Number.isFinite(body.cache_ttl) ? body.cache_ttl : 0,
    }
    const before = flatten(held?.values ?? {})
    this.entries.set(key, entry)
    this.persist()
    if (held) {
      const after = flatten(entry.values)
      const changed = [...new Set([...Object.keys(before), ...Object.keys(after)])]
        .filter((p) => JSON.stringify(before[p]) !== JSON.stringify(after[p]))
        .sort()
      if (changed.length > 0) this.emit(changed)
    }
    return entry
  }

  /** @internal @param {string} key */
  async revalidate(key) {
    try {
      await this.fetch(key)
    } catch {
      // Offline or refused: the copy held keeps serving, and the next read tries again.
    }
  }

  /** @internal @param {unknown} err */
  emitError(err) {
    for (const l of this.errorListeners) {
      try {
        l(err)
      } catch (e) {
        console.error('backd: a client.config error listener failed:', e)
      }
    }
  }

  /** @internal @param {string[]} paths */
  emit(paths) {
    for (const l of this.listeners) {
      try {
        l(paths)
      } catch (err) {
        console.error('backd: a client.config change listener failed:', err)
      }
    }
  }
}

/**
 * Whether a request key (comma separated branches; '' is everything) holds any of the changed keys.
 * @param {string} key
 * @param {string[]} changed
 */
function touches(key, changed) {
  if (key === '') return true
  return key.split(',').some((p) => changed.some((c) => c === p || c.startsWith(p + '.') || p.startsWith(c + '.')))
}

/**
 * The key of a request: its paths, trimmed, sorted and without repeats; '' for everything.
 * @param {string[] | string | undefined} paths
 */
function normalize(paths) {
  const list = (Array.isArray(paths) ? paths : String(paths ?? '').split(',')).map((p) => p.trim()).filter(Boolean)
  return [...new Set(list)].sort().join(',')
}

/**
 * The `max-age` a `304` repeats, if any, else the ttl held.
 * @param {Headers} headers
 * @param {number} fallback
 */
function ttlOf(headers, fallback) {
  const m = /max-age=(\d+)/i.exec(headers.get('Cache-Control') ?? '')
  return m ? Number(m[1]) : fallback
}

/** `localStorage` when there is one that works; undefined otherwise. */
function defaultStorage() {
  try {
    const ls = globalThis.localStorage
    if (!ls) return undefined
    ls.getItem(PREFIX) // blocked storage throws here
    return ls
  } catch {
    return undefined
  }
}
