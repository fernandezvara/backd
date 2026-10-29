import { Admin } from './admin.js'
import { Push } from './push.js'
import { Consents } from './consents.js'
import { Account } from './account.js'
import { Config } from './config.js'
import { Orgs } from './orgs.js'
import { Realtime } from './realtime.js'
import { Auth } from './auth.js'
import { Database } from './data.js'
import { xhrFetch } from './xhr.js'
import { RevalidationCache, cacheable } from './cache.js'
import { NetworkError, RetryableError, errorFromResponse } from './errors.js'
import { COOKIE_SESSION, memoryStorage } from './storage.js'

/**
 * @typedef {import('./storage.js').TokenStorage} TokenStorage
 */

/**
 * Retry settings. Only 429 and 503 answers are retried, and only for safe
 * requests: GET, or writes with If-Match.
 * @typedef {object} RetryOptions
 * @property {number} attempts       Extra attempts after the first (0 disables).
 * @property {number} [maxDelayMs]   Longest wait between attempts; default 30000.
 */

/**
 * @typedef {object} ClientOptions
 * @property {string} url                 Base URL of backd, e.g. "https://api.example.com".
 * @property {string} realm               The realm to talk to.
 * @property {string} [apiKey]            Server-side only: an API key (`bdk_…`). Full access to the realm.
 * @property {boolean} [dangerouslyAllowBrowser] Allow `apiKey` in a browser. Anyone who loads the page gets the key.
 * @property {string} [org]              The organization this client signs in to and works in, by id or by the slug people
 *   know (`'acme'`). Sign-in calls go to `/_auth/orgs/{org}/…`, so the session is scoped to it; the answer carries the id.
 *   Without it, the sign-in calls take `org` as an argument. See {@link Client#org} for an app that works in several.
 * @property {TokenStorage} [storage]     Where the session token lives; memory by default.
 * @property {boolean} [cookies]          Browser apps: keep the session in an HttpOnly cookie that scripts can't read,
 *   instead of a token. The realm must enable `sessions.cookie` and list the app's origin in `cors.origins`; the
 *   app's page (or the API) must be on a site the cookie's SameSite setting allows. The admin API doesn't take cookies.
 * @property {import('./oauth.js').OAuthStorage} [oauthStorage] Where the sign-in with a provider keeps its PKCE verifier while the
 *   user is at the provider; the tab's `sessionStorage` by default.
 * @property {boolean} [completeOnLoad] Browser apps: finish a sign-in with a provider (or a link) that the page's
 *   address carries (`backd_code`, `backd_error`, `backd_linked`) as soon as the client is created, and resolve
 *   `client.auth.ready` with the outcome. On by default; `false` leaves it to `client.auth.completeSignIn()`.
 * @property {RetryOptions} [retry]       Retries for 429/503; off by default.
 * @property {boolean | import('./cache.js').CacheOptions} [cache] Keep the last body and `ETag` of document and list
 *   reads in memory and revalidate them with `If-None-Match`, so an unchanged read costs a `304`. On by default;
 *   `false` turns it off, an object sets `maxEntries` (200) and `maxBytes` (5 MiB).
 * @property {import('./config.js').ConfigStorage | false} [configStorage] Where `client.config` keeps its copies between page loads;
 *   `localStorage` in browsers by default (a blocked one falls back to memory), `false` for memory only.
 * @property {typeof fetch} [fetch]       A fetch implementation; the global one by default.
 * @property {Record<string, string>} [headers] Extra headers for every request.
 * @property {string} [types]             Development builds: the fingerprint of the generated types the app was
 *   written against (`fingerprint` in `realm.json`, made by `backd types`). It is sent as `X-Backd-Types`; when the
 *   server's config has moved on it answers `X-Backd-Types-Stale` and the client warns once. Leave it out in production:
 *   nothing is sent then.
 * @property {() => void} [onStaleTypes]  Called, once, when the server says the types are stale; logs a warning by default.
 */

/**
 * How much of an upload has been sent.
 * @typedef {object} UploadProgress
 * @property {number} loaded   Bytes sent.
 * @property {number} total    Bytes in all, when known (0 otherwise).
 */

/**
 * Per-request options.
 * @typedef {object} RequestOptions
 * @property {AbortSignal} [signal]
 * @property {RetryOptions} [retry]
 * @property {Record<string, string>} [headers]
 * @property {boolean} [cache]   `false` skips the client's revalidation cache for this read.
 */

/**
 * @typedef {object} RequestInit
 * @property {string} method
 * @property {string[]} path             Path segments after /v1/{realm}, encoded here.
 * @property {Record<string, string | number | boolean | undefined>} [query]
 * @property {unknown} [body]            Sent as JSON.
 * @property {BodyInit} [rawBody]        Sent as it is (a file's bytes), instead of `body`.
 * @property {(p: UploadProgress) => void} [onUploadProgress]  Called as the body is sent (in a browser; elsewhere once, when it is sent).
 * @property {boolean} [raw]             Don't read the answer: it comes back as `response`, for a file's bytes.
 * @property {string} [contentType]      Defaults to application/json when there's a body (octet-stream with `rawBody`).
 * @property {Record<string, string>} [headers]
 * @property {boolean} [auth]            Send credentials; default true.
 * @property {boolean} [expire]          Treat a refused session token as expired; default true.
 * @property {boolean} [reauth]          A sensitive action: send the fresh proof `client.auth.reauth()` gave, while it is good.
 * @property {AbortSignal} [signal]
 * @property {RetryOptions} [retry]
 * @property {boolean} [cache]           `false` skips the revalidation cache for this read.
 */

/**
 * The raw answer of a successful request.
 * @typedef {object} RawResponse
 * @property {number} status
 * @property {Headers} headers
 * @property {any} data   Parsed JSON, or undefined for empty bodies.
 * @property {Response} [response]   With `raw`: the answer, its body not read.
 * @property {boolean} [fromCache]    The server answered `304` and `data` is the body kept from an earlier read.
 */

const isBrowser = () => typeof window !== 'undefined' && typeof window.document !== 'undefined'

/**
 * Creates a client for one realm. Pass the realm's generated `Realm` type (from `backd types`) to type
 * databases, collections, documents, `where`, `order_by`, function calls and configuration paths:
 * `createClient<Realm>({ url, realm })`. It changes nothing at run time; without it everything is untyped.
 * @template [R=undefined]
 * @param {ClientOptions} options
 * @returns {R extends import('./typed.js').RealmShape ? import('./typed.js').TypedClient<R> : Client}
 */
export function createClient(options) {
  return /** @type {any} */ (new Client(options))
}

/**
 * An organization as the person types it or the app keeps it: an id or a slug. Case and
 * surrounding spaces do not matter to the server, so they do not matter here.
 * @param {string | undefined} ref
 * @returns {string}
 */
export function normalizeOrgRef(ref) {
  return (ref ?? '').trim().toLowerCase()
}

export class Client {
  /** @type {Map<string, Client>} */
  #orgClients = new Map()
  /** @type {Client | undefined} */
  #root

  /**
   * @param {ClientOptions} options
   * @param {Client} [root]  The client this one was made from by {@link Client#org}.
   */
  constructor(options, root) {
    if (!options?.url) throw new TypeError('createClient: `url` is required')
    if (!options.realm) throw new TypeError('createClient: `realm` is required')
    if (options.apiKey && isBrowser() && !options.dangerouslyAllowBrowser) {
      throw new Error(
        'createClient: API keys give full access to the realm and must not be used in browsers, ' +
          'where every visitor can read them. Use sessions (client.auth) instead, or pass ' +
          '`dangerouslyAllowBrowser: true` if you really mean it.',
      )
    }
    if (options.cookies && options.apiKey) {
      throw new TypeError('createClient: `cookies` is for sessions in a browser; an API key is sent in a header')
    }
    /** @internal */
    this.options = options
    this.#root = root
    /**
     * The organization this client is bound to, as it was given (an id or a slug), or "".
     * @readonly
     */
    this.orgRef = normalizeOrgRef(options.org)
    /** @readonly */
    this.url = options.url.replace(/\/+$/, '')
    /** @readonly */
    this.realm = options.realm
    /** @internal */
    this.cookies = Boolean(options.cookies)
    /** @internal */
    this.apiKey = options.apiKey
    /** @internal */
    this.fetchImpl = options.fetch ?? globalThis.fetch.bind(globalThis)
    /** @internal */
    this.customFetch = Boolean(options.fetch)
    /** @internal */
    this.retry = options.retry ?? { attempts: 0 }
    /** @internal */
    this.headers = options.headers ?? {}
    /** @internal */
    this.types = options.types
    /** @internal */
    this.onStaleTypes = options.onStaleTypes
    /** @internal */
    this.staleReported = false
    /** @readonly */
    this.storage = options.storage ?? memoryStorage()
    /** @internal */
    this.oauthStorage = options.oauthStorage
    /**
     * The fresh proof of identity `client.auth.reauth()` gave, sent with the sensitive requests while it is good.
     * @internal
     * @type {{ token: string, expires: number } | undefined}
     */
    this.proof = undefined
    /**
     * Last body and ETag of document and list reads; absent when `cache: false`.
     * @internal
     * @type {RevalidationCache | undefined}
     */
    this.cache = options.cache === false ? undefined : new RevalidationCache(typeof options.cache === 'object' ? options.cache : {})
    /** Sign-up, login and sessions. */
    this.auth = new Auth(this)
    if (options.completeOnLoad !== false) this.auth.completeOnLoad()
    /** Users, roles, invitations and API keys; needs an admin `apiKey` or an admin user's session. */
    this.admin = new Admin(this)
    /** Push notifications: register this device so that functions can reach the signed-in user. */
    this.push = new Push(this)
    /** The consents the realm asks for: their texts, and what the signed-in user holds. */
    this.consents = new Consents(this)
    /** The signed-in user's own data: ask for a copy of everything the realm holds about them, and download it. */
    this.account = new Account(this)
    /** The realm's app configuration: flags, switches and limits, cached per user. */
    this.config = new Config(this, options.configStorage)
    /** Organizations of the signed-in user: members, teams and invitations. */
    this.orgs = new Orgs(this)
    /** Live lists: subscribe to the changes of collections that declare `realtime: true`. */
    this.realtime = new Realtime(this)
  }

  /** Whether the client was created with an API key. */
  get hasApiKey() {
    return Boolean(this.apiKey)
  }

  /**
   * How the revalidation cache is doing: reads the server answered `304` to
   * (`hits`), cacheable reads that came back in full (`misses`), and what is kept.
   * @returns {import('./cache.js').CacheStats}
   */
  cacheStats() {
    return this.cache?.stats() ?? { hits: 0, misses: 0, entries: 0, bytes: 0 }
  }

  /** Forgets everything the revalidation cache holds. */
  clearCache() {
    this.cache?.clear()
  }

  /**
   * The client of one organization, for an app that is signed in to several at once: it has the same
   * address, realm and settings and a token of its own, so its calls reach that organization and
   * no other, and signing out of it leaves the others signed in. The organization is its id or the
   * slug people know; asking again for the same gives the same client. It needs a storage that can
   * keep a token per organization (`memoryStorage()` and `localStorageStorage()` can). With
   * `cookies: true` the browser holds one session at a time, so an app that works in several
   * organizations uses tokens.
   * @param {string} ref
   * @returns {Client}
   */
  org(ref) {
    const key = normalizeOrgRef(ref)
    if (!key) throw new TypeError('client.org: the organization (an id or a slug) is required')
    const root = this.#root ?? this
    if (root.orgRef === key) return root
    let c = root.#orgClients.get(key)
    if (!c) {
      if (root.cookies) throw new TypeError('client.org: with `cookies: true` the browser keeps one session; create a client per organization or use tokens')
      if (!root.storage.slot) throw new TypeError('client.org: this storage cannot keep a token per organization; use memoryStorage() or localStorageStorage()')
      c = new Client({ ...root.options, org: key, storage: root.storage.slot(key), completeOnLoad: false }, root)
      root.#orgClients.set(key, c)
    }
    return c
  }

  /**
   * The client that made this one (the one without an organization), or this client.
   * @internal
   * @returns {Client}
   */
  get root() {
    return this.#root ?? this
  }

  /**
   * A database of the realm, to reach its collections:
   * `client.db('main').collection('posts')`.
   * @param {string} name
   * @returns {Database}
   */
  db(name) {
    return new Database(this, name)
  }

  /**
   * Sends one request. In a browser, an upload that reports its progress goes through
   * XMLHttpRequest, the only way to know how much has been sent; everywhere else, fetch.
   * @internal
   * @param {URL} url
   * @param {((p: UploadProgress) => void) | undefined} onProgress
   * @param {{ method: string, headers: Record<string, string>, body?: any, signal?: AbortSignal, credentials?: 'include', duplex?: string }} init
   * @returns {Promise<Response>}
   */
  async send(url, onProgress, init) {
    if (!onProgress) return this.fetchImpl(url, init)
    if (typeof XMLHttpRequest !== 'undefined' && !this.customFetch) return xhrFetch(url, init, onProgress)
    const size = init.body instanceof Blob ? init.body.size : /** @type {any} */ (init.body)?.byteLength ?? 0
    const res = await this.fetchImpl(url, init)
    onProgress({ loaded: size, total: size })
    return res
  }

  /**
   * Opens a server-sent event stream: a POST to /v1/{realm}/… that answers `text/event-stream`. The
   * answer is returned unread; a refusal throws the error the API sent.
   * @internal
   * @param {{ path: string[], body: unknown, signal?: AbortSignal }} req
   * @returns {Promise<Response>}
   */
  async openStream(req) {
    const url = new URL(this.url + '/v1/' + [this.realm, ...req.path].map(encodeURIComponent).join('/'))
    /** @type {Record<string, string>} */
    const headers = { Accept: 'text/event-stream', 'Content-Type': 'application/json', ...this.headers }
    if (this.types) headers['X-Backd-Types'] = this.types
    let sentSession = false
    if (this.apiKey) headers.Authorization = 'Bearer ' + this.apiKey
    else {
      const token = await this.storage.get()
      if (token) {
        if (token !== COOKIE_SESSION) headers.Authorization = 'Bearer ' + token
        sentSession = true
      }
    }
    /** @type {Response} */
    let res
    try {
      res = await this.fetchImpl(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(req.body),
        signal: req.signal,
        credentials: this.cookies ? 'include' : undefined,
      })
    } catch (cause) {
      throw new NetworkError({
        status: 0,
        code: req.signal?.aborted ? 'aborted' : 'network_error',
        message: req.signal?.aborted ? 'request aborted' : 'network error: ' + String(cause),
        cause,
      })
    }
    if (res.ok) return res
    const err = await errorFromResponse(res)
    if (isSessionGone(err) && sentSession) await this.auth._expired()
    throw err
  }

  /**
   * Sends a request to /v1/{realm}/…, adding credentials, and returns the
   * parsed answer. Non-2xx answers throw a BackdError.
   * @param {RequestInit} req
   * @returns {Promise<RawResponse>}
   */
  async request(req) {
    const url = new URL(this.url + '/v1/' + [this.realm, ...req.path].map(encodeURIComponent).join('/'))
    for (const [k, v] of Object.entries(req.query ?? {})) {
      if (v !== undefined) url.searchParams.set(k, String(v))
    }
    /** @type {Record<string, string>} */
    const headers = { Accept: req.raw ? '*/*' : 'application/json', ...this.headers, ...req.headers }
    if (req.rawBody !== undefined) headers['Content-Type'] = req.contentType ?? 'application/octet-stream'
    else if (req.body !== undefined) headers['Content-Type'] = req.contentType ?? 'application/json'

    if (this.types) headers['X-Backd-Types'] = this.types
    if (req.reauth && this.proof && this.proof.expires > Date.now()) headers['X-Backd-Reauth'] = this.proof.token
    let sentSession = false
    /** The signed-in caller as the cache knows it: entries read as another caller are never reused. */
    let owner = ''
    if (req.auth !== false) {
      if (this.apiKey) {
        headers.Authorization = 'Bearer ' + this.apiKey
        owner = 'key:' + this.apiKey
      } else {
        const token = await this.storage.get()
        if (token) {
          if (token !== COOKIE_SESSION) headers.Authorization = 'Bearer ' + token // a cookie session is sent by the browser
          sentSession = true
          owner = 'session:' + token
        }
      }
    }

    // Revalidation: send the tag of the copy we hold, if any.
    const cache = this.cache && req.cache !== false && cacheable(req) ? this.cache : undefined
    const key = url.href
    /** @type {import('./cache.js').Entry | undefined} */
    let held
    if (cache) {
      cache.scope(owner)
      // A caller's own If-None-Match is theirs to handle: the 304 goes back to them.
      if (!Object.keys(headers).some((h) => h.toLowerCase() === 'if-none-match')) {
        held = cache.get(key)
        if (held) headers['If-None-Match'] = held.etag
      }
    }
    const writesDocument = req.method !== 'GET' && req.method !== 'HEAD' && req.path.length === 3 && !req.path[0].startsWith('_') && !req.path[1].startsWith('_')

    const retry = req.retry ?? this.retry
    // Safe to repeat: reads, writes conditional on a version, and requests the server deduplicates by key.
    const safe = req.method === 'GET' || Object.keys(headers).some((h) => ['if-match', 'idempotency-key'].includes(h.toLowerCase()))
    const attempts = safe ? Math.max(0, retry.attempts) : 0
    for (let attempt = 0; ; attempt++) {
      /** @type {Response} */
      let res
      try {
        res = await this.send(url, req.onUploadProgress, {
          method: req.method,
          headers,
          body: req.rawBody !== undefined ? req.rawBody : req.body === undefined ? undefined : JSON.stringify(req.body),
          signal: req.signal,
          credentials: this.cookies ? 'include' : undefined,
          // A stream is sent as it is read, which fetch asks to be told about.
          ...(typeof ReadableStream !== 'undefined' && req.rawBody instanceof ReadableStream ? { duplex: 'half' } : {}),
        })
      } catch (cause) {
        throw new NetworkError({
          status: 0,
          code: req.signal?.aborted ? 'aborted' : 'network_error',
          message: req.signal?.aborted ? 'request aborted' : 'network error: ' + String(cause),
          cause,
        })
      }
      if (this.types && !this.staleReported && res.headers.get('X-Backd-Types-Stale') === 'true') {
        this.staleReported = true
        if (this.onStaleTypes) this.onStaleTypes()
        else console.warn('backd: the generated types are out of date with the server: run `backd types` and commit the result')
      }
      if (res.status === 304 && cache && held) {
        cache.hits++
        // The copy we hold is current; its headers (ETag, …) are the ones the 304 repeats.
        return { status: 200, headers: held.headers, data: JSON.parse(held.text), fromCache: true }
      }
      // The caller asked with their own If-None-Match: the 304 is their answer.
      if (res.status === 304) return { status: 304, headers: res.headers, data: undefined }
      if (res.ok && writesDocument) this.cache?.dropPath(url.pathname) // a write changed it: what we hold is stale
      if (res.ok && req.raw) return { status: res.status, headers: res.headers, data: undefined, response: res }
      if (res.ok) {
        const text = await res.text()
        if (cache) {
          cache.misses++
          const etag = res.headers.get('ETag')
          const noStore = /no-store/i.test(res.headers.get('Cache-Control') ?? '')
          if (etag && !noStore && text) cache.set(key, { etag, text, headers: res.headers })
          else cache.delete(key)
        }
        return { status: res.status, headers: res.headers, data: text ? JSON.parse(text) : undefined }
      }
      const err = await errorFromResponse(res)
      if (isSessionGone(err) && sentSession && req.expire !== false) {
        await this.auth._expired()
      }
      if (err instanceof RetryableError && attempt < attempts) {
        await sleep(backoff(attempt, err.retryAfter, retry.maxDelayMs ?? 30000), req.signal)
        continue
      }
      throw err
    }
  }
}

/**
 * Wait before the next attempt: what the server asked, else exponential
 * backoff from 500 ms; never more than max.
 * @param {number} attempt
 * @param {number | undefined} retryAfter
 * @param {number} max
 */
function backoff(attempt, retryAfter, max) {
  return Math.min(retryAfter ?? 500 * 2 ** attempt, max)
}

/**
 * @param {number} ms
 * @param {AbortSignal} [signal]
 * @returns {Promise<void>}
 */
function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason)
    const t = setTimeout(resolve, ms)
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(t)
        reject(signal.reason)
      },
      { once: true },
    )
  })
}

/**
 * Whether the server no longer accepts the session: it expired or was ended (`unauthenticated`), or
 * it belongs to no organization in a realm that has them (`session_unscoped`, a session from before
 * sessions were scoped). Either way the person signs in again.
 * @param {{ code?: string }} err
 */
function isSessionGone(err) {
  return err.code === 'unauthenticated' || err.code === 'session_unscoped'
}
