import { Admin } from './admin.js'
import { Auth } from './auth.js'
import { Database } from './data.js'
import { xhrFetch } from './xhr.js'
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
 * @property {TokenStorage} [storage]     Where the session token lives; memory by default.
 * @property {boolean} [cookies]          Browser apps: keep the session in an HttpOnly cookie that scripts can't read,
 *   instead of a token. The realm must enable `sessions.cookie` and list the app's origin in `cors.origins`; the
 *   app's page (or the API) must be on a site the cookie's SameSite setting allows. The admin API doesn't take cookies.
 * @property {import('./oauth.js').OAuthStorage} [oauthStorage] Where the sign-in with a provider keeps its PKCE verifier while the
 *   user is at the provider; the tab's `sessionStorage` by default.
 * @property {RetryOptions} [retry]       Retries for 429/503; off by default.
 * @property {typeof fetch} [fetch]       A fetch implementation; the global one by default.
 * @property {Record<string, string>} [headers] Extra headers for every request.
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
 * @property {AbortSignal} [signal]
 * @property {RetryOptions} [retry]
 */

/**
 * The raw answer of a successful request.
 * @typedef {object} RawResponse
 * @property {number} status
 * @property {Headers} headers
 * @property {any} data   Parsed JSON, or undefined for empty bodies.
 * @property {Response} [response]   With `raw`: the answer, its body not read.
 */

const isBrowser = () => typeof window !== 'undefined' && typeof window.document !== 'undefined'

/**
 * Creates a client for one realm.
 * @param {ClientOptions} options
 * @returns {Client}
 */
export function createClient(options) {
  return new Client(options)
}

export class Client {
  /** @param {ClientOptions} options */
  constructor(options) {
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
    /** @readonly */
    this.storage = options.storage ?? memoryStorage()
    /** @internal */
    this.oauthStorage = options.oauthStorage
    /** Sign-up, login and sessions. */
    this.auth = new Auth(this)
    /** Users, roles, invitations and API keys; needs an admin `apiKey` or an admin user's session. */
    this.admin = new Admin(this)
  }

  /** Whether the client was created with an API key. */
  get hasApiKey() {
    return Boolean(this.apiKey)
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

    let sentSession = false
    if (req.auth !== false) {
      if (this.apiKey) {
        headers.Authorization = 'Bearer ' + this.apiKey
      } else {
        const token = await this.storage.get()
        if (token) {
          if (token !== COOKIE_SESSION) headers.Authorization = 'Bearer ' + token // a cookie session is sent by the browser
          sentSession = true
        }
      }
    }

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
      if (res.ok && req.raw) return { status: res.status, headers: res.headers, data: undefined, response: res }
      if (res.ok) {
        const text = await res.text()
        return { status: res.status, headers: res.headers, data: text ? JSON.parse(text) : undefined }
      }
      const err = await errorFromResponse(res)
      if (err.code === 'unauthenticated' && sentSession && req.expire !== false) {
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
