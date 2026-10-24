/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * An export of the signed-in user's personal data.
 * @typedef {object} DataExport
 * @property {string} id
 * @property {'queued' | 'running' | 'ready' | 'failed' | 'expired'} status  `ready` can be downloaded until `expires_at`; `failed` may be asked for again at once.
 * @property {string} created_at
 * @property {string} [finished_at]
 * @property {string} [expires_at]  When the package is deleted (ready only).
 * @property {number} [size]        Bytes of the zip (ready only).
 * @property {Record<string, number>} [counts]  What it holds (ready only): records per part, and `files`.
 * @property {string} [step]        What it is doing now (queued or running, as `account.export(id)` shows it).
 */

/**
 * A ready export, to save.
 * @typedef {object} ExportDownload
 * @property {string} [url]         A link signed for a few minutes, when the realm delivers by link: open it (`location.assign(url)`) or fetch it.
 * @property {string} [expires_at]  When the link stops working.
 * @property {() => Promise<Uint8Array>} bytes  The zip's bytes: read from the link, or from the answer when the realm sends them itself.
 */

/**
 * The signed-in user's own account data: `client.account`. A person can download a copy of everything the realm holds about them
 * (documents, files and account data, with a `README.html`): ask for it, follow it, and save it when it is ready.
 *
 * ```js
 * await client.auth.reauth({ method: 'password', password })   // a fresh proof, as for any sensitive action
 * const job = await client.account.requestExport()               // { id, status: 'queued', … }
 * const ready = await client.account.waitForExport(job.id)       // polls until it is ready
 * const { url } = await client.account.downloadExport(ready.id)  // a link signed for five minutes
 * ```
 *
 * The realm must set `compliance.export: true` (`NotFoundError` otherwise). A user may ask once every 24 hours:
 * a second request is a `RetryableError` (`429`) whose `retryAfter` says, in milliseconds, how long to wait. When the package is ready the user is also emailed a link to the
 * app's download page (`compliance.export_page?export=<id>`).
 */
export class Account {
  /** @param {Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
  }

  /**
   * Asks for an export of the user's personal data. Needs a fresh proof: call `client.auth.reauth()` first
   * (a `reauth_required` error says so). It is built in the background.
   * @param {RequestOptions} [opts]
   * @returns {Promise<DataExport>}
   */
  async requestExport(opts) {
    return (await this.client.request({ method: 'POST', path: ['_auth', 'me', 'export'], reauth: true, ...opts })).data
  }

  /**
   * The exports the user asked for, newest first.
   * @param {RequestOptions} [opts]
   * @returns {Promise<DataExport[]>}
   */
  async exports(opts) {
    return (await this.client.request({ method: 'GET', path: ['_auth', 'me', 'exports'], cache: false, ...opts })).data.items
  }

  /**
   * One export, for a page that waits for it: `step` says what it is doing while it runs.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<DataExport>}
   */
  async export(id, opts) {
    return (await this.client.request({ method: 'GET', path: ['_auth', 'me', 'exports', id], cache: false, ...opts })).data
  }

  /**
   * Polls an export until it is `ready`. A `failed` or `expired` one is an `Error` (the user may ask again).
   * @param {string} id
   * @param {RequestOptions & { pollIntervalMs?: number, timeoutMs?: number, onProgress?: (e: DataExport) => void }} [opts]  Polls every 2 s for up to 30 minutes by default.
   * @returns {Promise<DataExport>}
   */
  async waitForExport(id, opts = {}) {
    const { pollIntervalMs = 2000, timeoutMs = 30 * 60 * 1000, onProgress, ...rest } = opts
    const end = Date.now() + timeoutMs
    for (;;) {
      const e = await this.export(id, rest)
      onProgress?.(e)
      if (e.status === 'ready') return e
      if (e.status === 'failed' || e.status === 'expired') throw new Error(`backd: the export ${e.status}${e.status === 'failed' ? ': ask for a new one' : ''}`)
      if (Date.now() + pollIntervalMs > end) throw new Error(`backd: the export is still ${e.status} after ${Math.round(timeoutMs / 1000)}s`)
      await new Promise((r) => setTimeout(r, pollIntervalMs))
    }
  }

  /**
   * Gives access to a ready export. `ConflictError` until it is ready, and `410 gone` once it has expired.
   * A realm that delivers by link answers with `url` (open it, or `bytes()` reads it); one whose storage clients cannot reach sends the
   * zip through the API, and `bytes()` reads it from the answer.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<ExportDownload>}
   */
  async downloadExport(id, opts) {
    const headers = { Accept: 'application/json', ...opts?.headers }
    const { response } = await this.client.request({ method: 'GET', path: ['_auth', 'me', 'exports', id, 'download'], raw: true, ...opts, headers })
    if (!response) throw new Error('backd: no response')
    if (!(response.headers.get('Content-Type') ?? '').startsWith('application/json')) {
      return { bytes: async () => new Uint8Array(await response.arrayBuffer()) }
    }
    const link = await response.json()
    return {
      url: link.url,
      expires_at: link.expires_at,
      bytes: async () => {
        const res = await this.client.fetchImpl(link.url)
        if (!res.ok) throw new Error(`backd: the export link answered ${res.status}`)
        return new Uint8Array(await res.arrayBuffer())
      },
    }
  }
}
