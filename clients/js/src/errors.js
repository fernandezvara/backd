/**
 * @typedef {object} ErrorDetail
 * @property {string} path   Field or parameter the problem is about.
 * @property {string} reason What's wrong with it.
 */

/**
 * An error answered by backd, or a network failure (status 0).
 * `code` is stable and meant for programs; `message` is for humans.
 */
export class BackdError extends Error {
  /**
   * @param {object} init
   * @param {number} init.status      HTTP status; 0 for network failures.
   * @param {string} init.code        backd error code, e.g. "validation_error".
   * @param {string} init.message
   * @param {ErrorDetail[]} [init.details]
   * @param {string} [init.requestId] The request ID, to match server logs.
   * @param {unknown} [init.cause]
   */
  constructor({ status, code, message, details = [], requestId, cause }) {
    super(message, cause === undefined ? undefined : { cause })
    this.name = new.target.name
    /** @type {number} */
    this.status = status
    /** @type {string} */
    this.code = code
    /** @type {ErrorDetail[]} */
    this.details = details
    /** @type {string | undefined} */
    this.requestId = requestId
  }
}

/** 400: invalid body, query or header (`validation_error`, `invalid_json`, `invalid_query`, `invalid_header`). */
export class ValidationError extends BackdError {}

/** 401: missing, invalid or expired credentials, or a wrong email or password. */
export class AuthenticationError extends BackdError {}

/** 403: the caller isn't allowed to do this. */
export class ForbiddenError extends BackdError {}

/** 404: unknown realm, collection, document, user or session. */
export class NotFoundError extends BackdError {}

/** 409: a unique value already exists (`conflict`, `email_taken`) or concurrent writes (`write_conflict`). */
export class ConflictError extends BackdError {}

/** 412: `If-Match` didn't match the current version. */
export class VersionMismatchError extends BackdError {}

/** 429 and 503: try again later; `retryAfter` is in milliseconds when the server said. */
export class RetryableError extends BackdError {
  /**
   * @param {ConstructorParameters<typeof BackdError>[0] & { retryAfter?: number }} init
   */
  constructor(init) {
    super(init)
    /** @type {number | undefined} */
    this.retryAfter = init.retryAfter
  }
}

/** 0: the request didn't get an answer (network error, abort, timeout). */
export class NetworkError extends BackdError {}

/** @type {Record<number, typeof BackdError>} */
const byStatus = {
  400: ValidationError,
  401: AuthenticationError,
  403: ForbiddenError,
  404: NotFoundError,
  409: ConflictError,
  412: VersionMismatchError,
  429: RetryableError,
  503: RetryableError,
}

/**
 * The `BackdError` subclass for a status, or the base class if there's
 * no specific one for it. Used for real responses (`errorFromResponse`)
 * and to reconstruct the same shape from an async job's stored result,
 * which never went through an HTTP response of its own (roadmap F14).
 * @param {number} status
 * @returns {typeof BackdError}
 */
export function errorClassFor(status) {
  return byStatus[status] ?? BackdError
}

/**
 * Builds the error for a non-2xx response.
 * @param {Response} res
 * @returns {Promise<BackdError>}
 */
export async function errorFromResponse(res) {
  /** @type {{ error?: { code?: string, message?: string, details?: ErrorDetail[], request_id?: string } }} */
  let body = {}
  try {
    body = await res.json()
  } catch {
    // Not JSON (for example from a proxy): keep the defaults below.
  }
  const e = body.error ?? {}
  const Class = errorClassFor(res.status)
  const retryAfter = parseRetryAfter(res.headers.get('Retry-After'))
  return new Class({
    status: res.status,
    code: e.code ?? 'http_' + res.status,
    message: e.message ?? res.statusText ?? 'request failed',
    details: e.details ?? [],
    requestId: e.request_id ?? res.headers.get('X-Request-ID') ?? undefined,
    ...(Class === RetryableError ? { retryAfter } : {}),
  })
}

/**
 * Parses a Retry-After header (seconds or an HTTP date) into milliseconds.
 * @param {string | null} value
 * @returns {number | undefined}
 */
export function parseRetryAfter(value) {
  if (!value) return undefined
  if (/^\d+$/.test(value)) return Number(value) * 1000
  const at = Date.parse(value)
  return Number.isNaN(at) ? undefined : Math.max(0, at - Date.now())
}
