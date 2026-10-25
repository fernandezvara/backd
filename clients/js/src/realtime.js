import { AuthenticationError, BackdError, ForbiddenError, InvalidResumeTokenError, NetworkError, RealtimeNotEnabledError, SessionEndedError, ValidationError } from './errors.js'

/**
 * @typedef {'connecting' | 'live' | 'resyncing' | 'closed'} LiveStatus
 *   `connecting`: the stream is opening (or reopening) and nothing has been loaded yet; `live`: loaded and
 *   following; `resyncing`: reloading after a reset or a reconnection, while the last list stays available;
 *   `closed`: stopped, by `close()` or by an error.
 */

/**
 * What to follow, and how to load it first.
 * @typedef {object} SubscriptionSpec
 * @property {string} [id]           Names the subscription in `state.subscriptions`; default its position.
 * @property {string} db             The database.
 * @property {string} collection     A collection that declares `realtime: true`.
 * @property {object} [where]        The filter, in the query language limited to comparisons, `$in`, `$nin`,
 *   `$between` and `$isNull` with `$and`/`$or`. Subscribe with an equality on a grouping field (`_meta.team`)
 *   to keep the server's work small.
 * @property {string} [doc]          Follow one document by id instead of a filter.
 * @property {{ sort?: string | string[], limit?: number }} [load] The initial list and how the live list is kept:
 *   `sort` (`'-_meta.created_at'`, default `id`) orders it, new documents are inserted in order, and `limit`
 *   (default 100) is the most it holds.
 * @property {Record<string, ReferenceSpec>} [references] Referenced documents to keep beside the list, by name.
 */

/**
 * A reference the list's documents carry: the field holds the referenced document's id (or an array of ids).
 * @typedef {object} ReferenceSpec
 * @property {string} field         A field of the listed documents (a dot path).
 * @property {string} [db]          The referenced collection's database; default the subscription's.
 * @property {string} collection
 * @property {string[]} [fields]    Keep only these fields of each referenced document (and its `id`, `_meta`).
 */

/**
 * @typedef {object} SubscribeOptions
 * @property {number} [jitterMs]    A reload after a reset waits a random time up to this many ms (default 1000),
 *   so a reset of many clients doesn't make them all read at once.
 * @property {() => number} [random] Source of randomness for the jitter (default `Math.random`).
 * @property {number} [maxBackoffMs] Longest wait before reconnecting after a failure (default 30000).
 */

/**
 * @typedef {object} LiveSubscriptionState
 * @property {Record<string, any>[]} items   The documents, in order.
 * @property {LiveStatus} status
 */

/**
 * @typedef {object} LiveState
 * @property {LiveStatus} status
 * @property {Record<string, any>[]} items   The first subscription's documents (the usual case: one).
 * @property {Record<string, LiveSubscriptionState>} subscriptions  Every subscription, by id.
 * @property {Record<string, Record<string, Record<string, any>>>} references  Referenced documents: by reference
 *   name, then id.
 */

const RECONNECT_BASE_MS = 500

/** The id the `_config` subscription has on the wire. */
const CONFIG_ID = '_config'

/**
 * Who hears of the configuration on the stream.
 * @typedef {object} ConfigFollower
 * @property {string[]} paths                 The branches followed; none: everything the caller reads.
 * @property {() => void} ready               The stream is open (again): what was missed while it was down is read now.
 * @property {(paths: string[]) => void} changed  Keys that changed; none: any of them might have.
 * @property {(err: unknown) => void} error   The subscription was refused or the stream ended for good.
 */

/**
 * Live lists over one stream per client: the client multiplexes every subscription of every view over
 * `POST /v1/{realm}/_realtime`, loads each list stream-first (events received while the list loads are applied
 * after it, ignoring versions that are not newer), keeps it sorted, resumes after a disconnection, reloads
 * subscriptions the server resets (with jitter), reconnects at the end of the credential's lifetime and
 * reopens the stream when the set of subscriptions changes.
 */
export class Realtime {
  /** @param {import('./client.js').Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
    /** @internal @type {Map<string, Sub>} */
    this.subs = new Map()
    /** @internal */
    this.seq = 0
    /** @internal @type {string | undefined} */
    this.token = undefined
    /** @internal */
    this.gen = 0
    /** @internal @type {AbortController | undefined} */
    this.controller = undefined
    /** @internal */
    this.syncing = false
    /** @internal */
    this.ready = false
    /** @internal */
    this.running = false
    /** @internal @type {Set<LiveView>} */
    this.views = new Set()
    /** @internal @type {ConfigFollower | undefined} */
    this.config = undefined
  }

  /**
   * Adds a `_config` subscription to the stream, which tells the follower when what the caller reads of the
   * app configuration changes. One follower at a time; `client.config.live()` is what uses it.
   * @internal
   * @param {ConfigFollower} follower
   * @returns {() => void} Removes it.
   */
  followConfig(follower) {
    this.config = follower
    this.schedule()
    return () => {
      if (this.config !== follower) return
      this.config = undefined
      this.schedule()
    }
  }

  /**
   * Follows collections live. The returned view loads each subscription, keeps its list current and tells you
   * when it changes:
   *
   * ```js
   * const live = client.realtime.subscribe([
   *   { db: 'main', collection: 'items', where: { '_meta.team': teamId }, load: { sort: '-_meta.created_at', limit: 50 } },
   * ])
   * live.on('change', (state) => render(state.items))
   * // later
   * live.close()
   * ```
   * @param {SubscriptionSpec[]} specs
   * @param {SubscribeOptions} [options]
   * @returns {LiveView}
   */
  subscribe(specs, options = {}) {
    if (!Array.isArray(specs) || specs.length === 0) throw new TypeError('realtime.subscribe: at least one subscription is required')
    const view = new LiveView(this, options)
    const names = new Set()
    specs.forEach((spec, i) => {
      if (!spec?.db || !spec.collection) throw new TypeError('realtime.subscribe: each subscription needs `db` and `collection`')
      const name = spec.id ?? String(i)
      if (names.has(name)) throw new TypeError(`realtime.subscribe: duplicate subscription id "${name}"`)
      names.add(name)
      const sub = new Sub(`s${++this.seq}`, name, spec, view)
      view.subs.push(sub)
      this.subs.set(sub.wire, sub)
    })
    this.views.add(view)
    this.schedule()
    return view
  }

  /**
   * @internal
   * @param {LiveView} view
   */
  remove(view) {
    this.views.delete(view)
    for (const s of view.subs) this.subs.delete(s.wire)
    this.schedule()
  }

  /** The number of open views. */
  get open() {
    return this.views.size
  }

  /** @internal Reconciles the stream with the subscriptions, once per burst of changes. */
  schedule() {
    if (this.syncing) return
    this.syncing = true
    queueMicrotask(() => {
      this.syncing = false
      this.reconcile()
    })
  }

  /** @internal */
  reconcile() {
    if (this.subs.size === 0 && !this.config) {
      this.gen++
      this.controller?.abort()
      this.controller = undefined
      this.running = false
      this.ready = false
      return
    }
    // The set changed (or this is the first subscription): a new stream with the same resume token.
    this.gen++
    this.controller?.abort()
    this.ready = false
    for (const v of this.views) v.status()
    const gen = this.gen
    this.controller = new AbortController()
    this.running = true
    void this.connect(gen, this.controller)
  }

  /**
   * @internal
   * @param {number} gen
   * @param {AbortController} controller
   */
  async connect(gen, controller) {
    let attempt = 0
    while (gen === this.gen) {
      let reconnectNow = false
      try {
        /** @type {Array<Record<string, unknown>>} */
        const subscriptions = [...this.subs.values()].map((s) => ({
          id: s.wire,
          db: s.spec.db,
          collection: s.spec.collection,
          ...(s.spec.doc !== undefined ? { doc: s.spec.doc } : s.spec.where ? { where: s.spec.where } : {}),
        }))
        if (this.config) subscriptions.push({ id: CONFIG_ID, config: { paths: this.config.paths } })
        const body = { subscriptions, ...(this.token ? { resume_from: this.token } : {}) }
        const res = await this.client.openStream({ path: ['_realtime'], body, signal: controller.signal })
        attempt = 0
        reconnectNow = await this.read(res, gen)
      } catch (err) {
        if (gen !== this.gen) return
        if (err instanceof InvalidResumeTokenError) {
          // The token is not good (another credential, a rotated key): start over from a reload.
          this.token = undefined
          for (const s of this.subs.values()) s.needsLoad = true
          continue
        }
        if (err instanceof RealtimeNotEnabledError || err instanceof ForbiddenError || err instanceof ValidationError || err instanceof AuthenticationError) {
          // A refusal that names a subscription ends the view that asked for it; the others go on.
          const at = /^subscriptions\[(\d+)\]/.exec(err.details?.[0]?.path ?? '')
          const bad = at ? [...this.subs.values()][Number(at[1])] : undefined
          if (at && !bad && this.config && Number(at[1]) === this.subs.size) {
            // The refusal is of the configuration subscription (the realm has none): only it ends.
            const follower = this.config
            this.config = undefined
            follower.error(err)
            if (this.subs.size === 0) return
            continue
          }
          if (bad && this.views.size > 1) {
            bad.view.fail(err)
            this.token = this.token // unchanged: the rest resume where they were
            continue
          }
          this.fail(err)
          return
        }
        if (!(err instanceof NetworkError) && !(err instanceof BackdError)) throw err
      }
      if (gen !== this.gen) return
      this.ready = false
      for (const v of this.views) v.status()
      if (reconnectNow) continue
      const wait = Math.min(RECONNECT_BASE_MS * 2 ** attempt++, this.maxBackoff())
      await new Promise((r) => setTimeout(r, wait))
    }
  }

  /** @internal */
  maxBackoff() {
    let max = 30000
    for (const v of this.views) max = Math.min(max, v.options.maxBackoffMs ?? 30000)
    return max
  }

  /**
   * @internal
   * @param {unknown} err
   */
  fail(err) {
    const follower = this.config
    this.config = undefined
    follower?.error(err)
    this.gen++
    this.running = false
    this.ready = false
    for (const v of [...this.views]) v.fail(err)
  }

  /**
   * Reads the stream until it ends. Returns true when the next stream should open at once (the server asked:
   * `reauth`, or a reset that needs the subscription made again).
   * @internal
   * @param {Response} res
   * @param {number} gen
   * @returns {Promise<boolean>}
   */
  async read(res, gen) {
    if (!res.body) return false
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) return false
        buf += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
        let i
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i)
          buf = buf.slice(i + 2)
          const ev = parseBlock(block)
          if (!ev) continue
          if (gen !== this.gen) return false
          const again = this.handle(ev)
          if (again !== undefined) return again
        }
      }
    } finally {
      try {
        await reader.cancel()
      } catch {
        // already closed
      }
    }
  }

  /**
   * @internal
   * @param {{ id?: string, event: string, data: any }} ev
   * @returns {boolean | undefined} what `read` returns, when the stream is over
   */
  handle(ev) {
    if (ev.id) this.token = ev.id
    switch (ev.event) {
      case 'ready':
        this.ready = true
        for (const s of this.subs.values()) if (s.needsLoad || !s.loaded) s.view.load(s, false)
        for (const v of this.views) v.status()
        this.config?.ready()
        return undefined
      case 'config_changed':
        this.config?.changed(Array.isArray(ev.data.paths) ? ev.data.paths : [])
        return undefined
      case 'created':
      case 'updated': {
        const s = this.subs.get(ev.data.sub)
        if (s) s.view.apply(s, { kind: 'doc', doc: ev.data.doc })
        return undefined
      }
      case 'removed': {
        const s = this.subs.get(ev.data.sub)
        if (s) s.view.apply(s, { kind: 'removed', id: ev.data.id, version: ev.data.version })
        return undefined
      }
      case 'reset': {
        let again = false
        for (const id of ev.data.subs ?? []) {
          const s = this.subs.get(id)
          if (!s) continue
          s.view.emit('reset', { subscription: s.name, reason: ev.data.reason })
          if (ev.data.reason === 'config_changed') {
            // The server dropped it: it has to be made again, on a new stream, and loaded after it.
            s.needsLoad = true
            again = true
          } else {
            s.view.load(s, true)
          }
        }
        return again ? true : undefined
      }
      case 'session_ended':
        this.fail(new SessionEndedError({ status: 401, code: 'session_ended', message: 'the session, user or API key behind the stream is gone: sign in again' }))
        return false
      case 'reauth':
        return true // the credential's lifetime is over: reconnect, which sends the current one
    }
    return undefined
  }
}

/**
 * @param {string} block
 * @returns {{ id?: string, event: string, data: any } | undefined}
 */
function parseBlock(block) {
  let id
  let event = ''
  const data = []
  for (const line of block.split('\n')) {
    if (line.startsWith(':')) continue
    if (line.startsWith('id:')) id = line.slice(3).trim()
    else if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
  }
  if (!event) return undefined
  let parsed = {}
  if (data.length) {
    try {
      parsed = JSON.parse(data.join('\n'))
    } catch {
      return undefined
    }
  }
  return { id, event, data: parsed }
}

class Sub {
  /**
   * @param {string} wire  The id the stream knows it by.
   * @param {string} name  The id the app knows it by.
   * @param {SubscriptionSpec} spec
   * @param {LiveView} view
   */
  constructor(wire, name, spec, view) {
    this.wire = wire
    this.name = name
    this.spec = spec
    this.view = view
    /** @type {Map<string, Record<string, any>>} */
    this.items = new Map()
    this.loaded = false
    this.loading = false
    this.needsLoad = true
    /** @type {Array<{ kind: 'doc', doc: any } | { kind: 'removed', id: string, version: number }>} */
    this.buffer = []
    this.generation = 0
    this.everLoaded = false
  }
}

/**
 * A live view of one or more subscriptions.
 */
export class LiveView {
  /**
   * @param {Realtime} rt
   * @param {SubscribeOptions} options
   */
  constructor(rt, options) {
    /** @internal */
    this.rt = rt
    /** @internal */
    this.options = options
    /** @internal @type {Sub[]} */
    this.subs = []
    /** @internal @type {Map<string, Set<(arg: any) => void>>} */
    this.handlers = new Map()
    /** @internal */
    this.closed = false
    /** @internal @type {unknown} */
    this.error = undefined
    /** @internal */
    this.lastStatus = /** @type {LiveStatus} */ ('connecting')
    /** @internal */
    this.emitPending = false
    /** @internal @type {Map<string, Map<string, any>>} */
    this.refs = new Map()
    /** @internal @type {Set<string>} */
    this.refsAsked = new Set()
    /** @internal */
    this.refsPending = false
    /** @internal @type {Map<string, { ref: ReferenceSpec, ids: string[] }> | undefined} */
    this.pendingRefs = undefined
    /** @internal @type {Array<(v: LiveView) => void>} */
    this.waiters = []
  }

  /**
   * Listens: `change` (with the new {@link LiveState}), `status` (a {@link LiveStatus}), `reset` (`{ subscription,
   * reason }`, when the server asked for a reload), `access_lost` (`{ subscription }`: a reload found the caller
   * may no longer read it, and the list is empty) and `error` (an error that stopped the view).
   * @param {'change' | 'status' | 'reset' | 'access_lost' | 'error'} event
   * @param {(arg: any) => void} fn
   * @returns {() => void} Stops listening.
   */
  on(event, fn) {
    let set = this.handlers.get(event)
    if (!set) this.handlers.set(event, (set = new Set()))
    set.add(fn)
    return () => set.delete(fn)
  }

  /**
   * @internal
   * @param {string} event
   * @param {any} [arg]
   */
  emit(event, arg) {
    for (const fn of this.handlers.get(event) ?? []) {
      try {
        fn(arg)
      } catch (e) {
        queueMicrotask(() => {
          throw e
        })
      }
    }
  }

  /** The state now. */
  get state() {
    return this.snapshot()
  }

  /** The first subscription's documents, in order. */
  get items() {
    return this.snapshot().items
  }

  /** Resolves with the state once every subscription has loaded; rejects if the view stops first. */
  get ready() {
    return new Promise((resolve, reject) => {
      if (this.error) return reject(this.error)
      if (this.computeStatus() === 'live') return resolve(this.snapshot())
      const off = this.on('status', (s) => {
        if (s === 'live') {
          off()
          offErr()
          resolve(this.snapshot())
        }
      })
      const offErr = this.on('error', (e) => {
        off()
        offErr()
        reject(e)
      })
    })
  }

  /** Stops following. Safe to call more than once. */
  close() {
    if (this.closed) return
    this.closed = true
    this.rt.remove(this)
    this.setStatus('closed')
  }

  /**
   * @internal
   * @param {unknown} err
   */
  fail(err) {
    if (this.closed) return
    this.error = err
    this.closed = true
    this.rt.views.delete(this)
    for (const s of this.subs) this.rt.subs.delete(s.wire)
    this.setStatus('closed')
    this.emit('error', err)
  }

  /** @internal */
  computeStatus() {
    if (this.closed) return /** @type {LiveStatus} */ ('closed')
    const everLive = this.subs.every((s) => s.everLoaded)
    const allLoaded = this.subs.every((s) => s.loaded)
    if (allLoaded && this.rt.ready) return 'live'
    return everLive || this.subs.some((s) => s.everLoaded) ? 'resyncing' : 'connecting'
  }

  /** @internal Reports a status change. */
  status() {
    this.setStatus(this.computeStatus())
  }

  /**
   * @internal
   * @param {LiveStatus} s
   */
  setStatus(s) {
    if (s === this.lastStatus) return
    this.lastStatus = s
    this.emit('status', s)
    this.scheduleChange()
  }

  /** @internal */
  scheduleChange() {
    if (this.emitPending) return
    this.emitPending = true
    queueMicrotask(() => {
      this.emitPending = false
      if (this.handlers.get('change')?.size) this.emit('change', this.snapshot())
    })
  }

  /** @internal @returns {LiveState} */
  snapshot() {
    /** @type {Record<string, LiveSubscriptionState>} */
    const subscriptions = {}
    for (const s of this.subs) subscriptions[s.name] = { items: ordered(s), status: s.loaded ? (this.rt.ready ? 'live' : 'resyncing') : s.everLoaded ? 'resyncing' : 'connecting' }
    /** @type {LiveState['references']} */
    const references = {}
    for (const [name, m] of this.refs) references[name] = Object.fromEntries(m)
    return { status: this.lastStatus, items: this.subs.length ? subscriptions[this.subs[0].name].items : [], subscriptions, references }
  }

  /**
   * Loads (or reloads) a subscription's list. Events that arrive meanwhile wait in its buffer and are applied
   * after, ignoring any whose version is not newer. A reload after a reset waits a random time first.
   * @internal
   * @param {Sub} s
   * @param {boolean} jitter
   */
  load(s, jitter) {
    if (this.closed) return
    s.needsLoad = false
    s.loaded = false
    s.buffer = []
    const generation = ++s.generation
    this.status()
    const run = async () => {
      if (this.closed || generation !== s.generation) return
      try {
        const items = await this.fetch(s)
        if (this.closed || generation !== s.generation) return
        s.items = new Map(items.map((d) => [d.id, d]))
        for (const ev of s.buffer) this.applyNow(s, ev)
        s.buffer = []
        s.loaded = true
        s.everLoaded = true
        this.trim(s)
        this.collectReferences(s)
        this.status()
        this.scheduleChange()
      } catch (err) {
        if (this.closed || generation !== s.generation) return
        if (err instanceof ForbiddenError && s.everLoaded) {
          // Access was lost since the list was read (a member removed from a team): the list is
          // empty for this caller now. The view stays, and loads again if a later reset says so.
          s.items = new Map()
          s.buffer = []
          s.loaded = true
          this.emit('access_lost', { subscription: s.name })
          this.status()
          this.scheduleChange()
          return
        }
        if (err instanceof NetworkError || (err instanceof BackdError && err.status >= 500)) {
          // The list could not be read now: try again, after a pause.
          setTimeout(() => this.load(s, false), 1000)
          return
        }
        this.fail(err)
      }
    }
    if (jitter) {
      const max = this.options.jitterMs ?? 1000
      const random = this.options.random ?? Math.random
      setTimeout(run, Math.floor(random() * max))
    } else {
      void run()
    }
  }

  /**
   * @internal
   * @param {Sub} s
   * @returns {Promise<Record<string, any>[]>}
   */
  async fetch(s) {
    const col = this.rt.client.db(s.spec.db).collection(s.spec.collection)
    if (s.spec.doc !== undefined) {
      try {
        return [await col.get(s.spec.doc)]
      } catch (err) {
        if (err instanceof BackdError && err.status === 404) return []
        throw err
      }
    }
    const want = s.spec.load?.limit ?? 100
    /** @type {Record<string, any>[]} */
    const out = []
    /** @type {string | undefined} */
    let after
    while (out.length < want) {
      const page = await col.list({ where: s.spec.where, orderBy: s.spec.load?.sort, limit: Math.min(100, want - out.length), after })
      out.push(...page.items)
      if (!page.has_more || !page.next_cursor) break
      after = page.next_cursor
    }
    return out
  }

  /**
   * @internal
   * @param {Sub} s
   * @param {{ kind: 'doc', doc: any } | { kind: 'removed', id: string, version: number }} ev
   */
  apply(s, ev) {
    if (this.closed) return
    if (!s.loaded) {
      s.buffer.push(ev)
      return
    }
    this.applyNow(s, ev)
    this.trim(s)
    if (ev.kind === 'doc') this.collectReferences(s)
    this.scheduleChange()
  }

  /**
   * @internal
   * @param {Sub} s
   * @param {{ kind: 'doc', doc: any } | { kind: 'removed', id: string, version: number }} ev
   */
  applyNow(s, ev) {
    if (ev.kind === 'removed') {
      const held = s.items.get(ev.id)
      if (held && versionOf(held) <= ev.version) s.items.delete(ev.id)
      return
    }
    const held = s.items.get(ev.doc.id)
    if (held && versionOf(held) >= versionOf(ev.doc)) return
    s.items.set(ev.doc.id, ev.doc)
  }

  /**
   * Keeps a list within its `limit`, dropping what sorts last.
   * @internal
   * @param {Sub} s
   */
  trim(s) {
    const limit = s.spec.load?.limit ?? 100
    if (s.spec.doc !== undefined || s.items.size <= limit) return
    const keep = ordered(s).slice(0, limit)
    s.items = new Map(keep.map((d) => [d.id, d]))
  }

  /**
   * Finds referenced ids the view doesn't hold and reads them in one batched read per reference.
   * @internal
   * @param {Sub} s
   */
  collectReferences(s) {
    const refs = s.spec.references
    if (!refs) return
    for (const [name, ref] of Object.entries(refs)) {
      const held = this.refs.get(name) ?? new Map()
      this.refs.set(name, held)
      for (const d of s.items.values()) {
        for (const id of idsAt(d, ref.field)) {
          const key = name + '\u0000' + id
          if (!held.has(id) && !this.refsAsked.has(key)) {
            this.refsAsked.add(key)
            this.pendingRefs ??= new Map()
            const list = this.pendingRefs.get(name) ?? { ref, ids: [] }
            list.ids.push(id)
            this.pendingRefs.set(name, list)
          }
        }
      }
    }
    if (this.pendingRefs?.size && !this.refsPending) {
      this.refsPending = true
      queueMicrotask(() => void this.fetchReferences(s))
    }
  }

  /**
   * @internal
   * @param {Sub} s
   */
  async fetchReferences(s) {
    this.refsPending = false
    const pending = this.pendingRefs
    this.pendingRefs = undefined
    if (!pending || this.closed) return
    for (const [name, { ref, ids }] of pending) {
      const col = this.rt.client.db(ref.db ?? s.spec.db).collection(ref.collection)
      const held = this.refs.get(name) ?? new Map()
      this.refs.set(name, held)
      for (let i = 0; i < ids.length; i += 100) {
        const chunk = ids.slice(i, i + 100)
        try {
          const page = await col.list({ where: { id: { $in: chunk } }, limit: chunk.length })
          for (const d of page.items) held.set(d.id, trimFields(d, ref.fields))
        } catch {
          // What can't be read stays unknown; the next change asks again.
          for (const id of chunk) this.refsAsked.delete(name + '\u0000' + id)
        }
      }
    }
    this.scheduleChange()
  }
}

/**
 * @param {Sub} s
 * @returns {Record<string, any>[]}
 */
function ordered(s) {
  const items = [...s.items.values()]
  if (s.spec.doc !== undefined) return items
  const keys = [s.spec.load?.sort ?? 'id'].flat().map((k) => String(k).split(',')).flat().filter(Boolean)
  const parsed = keys.map((k) => (k.startsWith('-') ? { path: k.slice(1), dir: -1 } : { path: k.startsWith('+') ? k.slice(1) : k, dir: 1 }))
  if (!parsed.some((p) => p.path === 'id')) parsed.push({ path: 'id', dir: 1 })
  return items.sort((a, b) => {
    for (const { path, dir } of parsed) {
      const c = compare(at(a, path), at(b, path))
      if (c !== 0) return c * dir
    }
    return 0
  })
}

/**
 * @param {any} a
 * @param {any} b
 */
function compare(a, b) {
  if (a === b) return 0
  if (a === undefined || a === null) return -1 // missing and null sort first, as in the database
  if (b === undefined || b === null) return 1
  return a < b ? -1 : a > b ? 1 : 0
}

/** @param {any} doc @param {string} path */
function at(doc, path) {
  let v = doc
  for (const p of path.split('.')) {
    if (v === null || typeof v !== 'object') return undefined
    v = v[p]
  }
  return v
}

/** @param {any} doc @param {string} field @returns {string[]} */
function idsAt(doc, field) {
  const v = at(doc, field)
  if (typeof v === 'string') return [v]
  if (Array.isArray(v)) return v.filter((x) => typeof x === 'string')
  return []
}

/** @param {any} doc */
function versionOf(doc) {
  return doc?._meta?.version ?? 0
}

/** @param {Record<string, any>} doc @param {string[] | undefined} fields */
function trimFields(doc, fields) {
  if (!fields) return doc
  /** @type {Record<string, any>} */
  const out = { id: doc.id, _meta: doc._meta }
  for (const f of fields) {
    const v = at(doc, f)
    if (v !== undefined) out[f] = v
  }
  return out
}
