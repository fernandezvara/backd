import { errorClassFor } from './errors.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./errors.js').ErrorDetail} ErrorDetail
 */

/**
 * How an `async` job ended, once `status` is `'done'` — the same shape
 * a `sync` call's own answer would carry for the same outcome.
 * @typedef {object} JobResult
 * @property {string} status `ok`, `function_error`, `timeout`, `memory`, `cpu`, `crash`, `output_too_large` or `bundle`.
 * @property {unknown} [output]     The function's output, when `status` is `ok`.
 * @property {number} [http_status] The status a `sync` call would have answered with; absent only when `status` is `ok`.
 * @property {string} [code]        The function's own code (`function_error`) or one of backd's own; absent only when `status` is `ok`.
 * @property {string} [message]     Absent only when `status` is `ok`.
 * @property {ErrorDetail[]} [details]
 * @property {number} duration_ms
 */

/**
 * A job as the API returns it: `POST .../_func/{name}`'s `202` body, or `GET .../_jobs/{id}`.
 * @typedef {object} JobData
 * @property {string} id
 * @property {string} function `<database>/<name>`.
 * @property {'queued' | 'running' | 'done'} status
 * @property {string} created_at
 * @property {number} [attempts] How many times a worker has started it.
 * @property {string | null} [next_attempt_at] When a failed attempt will be retried (the function's `retry` policy); null otherwise.
 * @property {JobResult | null} result
 */

/**
 * `job.wait()` gave up before the job finished (roadmap F14) — the job
 * itself is unaffected and still running; call `wait()` again, or poll
 * with `status()`, whenever you like.
 */
export class JobTimeoutError extends Error {
  /** @param {string} jobId */
  constructor(jobId) {
    super(`job ${jobId} did not finish within the given timeout`)
    this.name = 'JobTimeoutError'
    /** @readonly */
    this.jobId = jobId
  }
}

/**
 * A handle to an `async` function's job (roadmap F11, this client since
 * F14): `db.fn(name, input)` returns one instead of the output directly
 * when the function is `async`. Poll with `status()`, or use `wait()`
 * to get the output the same way a `sync` call's return value works.
 */
export class Job {
  /**
   * @param {Client} client
   * @param {string} database
   * @param {JobData} data
   */
  constructor(client, database, data) {
    /** @internal */
    this.client = client
    /** @internal */
    this.database = database
    /** @readonly */
    this.id = data.id
    /** @readonly The function this job runs, as `<database>/<name>`. */
    this.function = data.function
    /** @internal */
    this.data = data
  }

  /** The job's data as of the last `status()` or `wait()` call, or when it was created. */
  get raw() {
    return this.data
  }

  /**
   * Polls the job's current status.
   * @param {RequestOptions} [opts]
   * @returns {Promise<'queued' | 'running' | 'done'>}
   */
  async status(opts) {
    this.data = await this._fetch(opts)
    return this.data.status
  }

  /**
   * Polls until the job is done, then returns its output — the same
   * value a `sync` call would have returned — or throws a `BackdError`
   * matching what a `sync` call would have thrown for the same outcome
   * (the function's own `code`, when it threw `ctx.error(...)`).
   * @param {RequestOptions & { pollIntervalMs?: number, timeoutMs?: number }} [opts]
   *   `pollIntervalMs` default 500. Without `timeoutMs`, waits indefinitely.
   * @returns {Promise<unknown>}
   */
  async wait(opts = {}) {
    const { pollIntervalMs = 500, timeoutMs, ...rest } = opts
    const deadline = timeoutMs === undefined ? undefined : Date.now() + timeoutMs
    for (;;) {
      this.data = await this._fetch(rest)
      if (this.data.status === 'done') return outputOrThrow(this.data.result, this.id)
      if (deadline !== undefined && Date.now() >= deadline) throw new JobTimeoutError(this.id)
      await sleep(pollIntervalMs)
    }
  }

  /**
   * @internal
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobData>}
   */
  async _fetch(opts) {
    const { data } = await this.client.request({ method: 'GET', path: [this.database, '_jobs', this.id], ...opts })
    return data
  }
}

/**
 * @param {JobResult | null} result
 * @param {string} jobId
 * @returns {unknown}
 */
function outputOrThrow(result, jobId) {
  if (!result) throw new Error(`job ${jobId} is done but has no result`)
  if (result.status === 'ok') return result.output
  const Class = errorClassFor(result.http_status ?? 500)
  throw new Class({
    status: result.http_status ?? 500,
    code: result.code ?? 'function_failed',
    message: result.message ?? 'the function failed',
    details: result.details ?? [],
  })
}

/**
 * @param {number} ms
 * @returns {Promise<void>}
 */
function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
