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

/** 400 `realtime_not_enabled`: the collection doesn't declare `realtime: true` (or lost it). */
export class RealtimeNotEnabledError extends ValidationError {}

/** 400 `too_many_subscriptions`: a stream may hold only so many subscriptions. */
export class TooManySubscriptionsError extends ValidationError {}

/** 400 `invalid_resume_token`: a resume token is not valid for this caller; the client drops it and reloads. */
export class InvalidResumeTokenError extends ValidationError {}

/**
 * 401 `password_expired`: the password is right, but it was set before an administrator expired it
 * (`backd user expire-passwords`), so it opens nothing until the user chooses a new one. Only the
 * right password gets this answer (a wrong one is `invalid_credentials`). `resetEmailQueued` says
 * that backd queued the email with the link to choose a new password, so the app can say "we sent
 * you an email"; when it is false (the realm sends no email, or the limits held it back) offer
 * `auth.requestPasswordReset({ email })`, which is the same flow a user starts with *Forgot
 * password*, or tell them to ask their administrator. The session of a signed-in user is not
 * ended by this: it comes from signing in or from `auth.reauth({ method: 'password' })`.
 */
export class PasswordExpiredError extends AuthenticationError {
  /** @param {ConstructorParameters<typeof BackdError>[0]} init */
  constructor(init) {
    super(init)
    /** @type {boolean} */
    this.resetEmailQueued = (init.details ?? []).some((d) => d.path === 'reset_email')
  }
}

/**
 * The stream's session, user or key is gone (the server said `session_ended`). Live views stop and report this;
 * sign in again and subscribe again.
 */
export class SessionEndedError extends AuthenticationError {}

/** 403: the caller isn't allowed to do this. */
export class ForbiddenError extends BackdError {}

/**
 * Not an HTTP error: a sign-up in a realm that requires verified addresses
 * answers `202`, because the account exists but there is no session. Thrown
 * by `auth.signup()` so the code after it, which expects a session, doesn't run.
 */
export class VerificationRequiredError extends BackdError {}

/**
 * Not an HTTP error: a password login (or sign-up) that needs a second step before it gives a
 * session. `auth.login()` and `auth.signup()` promise a session, so they throw this instead;
 * `auth.signIn()` returns the step as a value. `code` is `mfa_required` (finish with
 * `auth.verifyMfa(error.challenge, method, code)`; `methods` says which),
 * `mfa_enrollment_required` (set a second factor up with `auth.mfa.setup({ challenge })`) or
 * `consent_required` (accept `consents` with `auth.acceptConsents(error.challenge, consents)`).
 */
export class SecondStepRequiredError extends BackdError {
  /**
   * @param {ConstructorParameters<typeof BackdError>[0] & { challenge: string, methods?: string[], consents?: Array<{ name: string, version: string }> }} init
   */
  constructor(init) {
    super(init)
    /** @type {string} */
    this.challenge = init.challenge
    /** @type {string[]} */
    this.methods = init.methods ?? []
    /** With `code: 'consent_required'`: the consents to accept, `{ name, version }`. */
    this.consents = init.consents ?? []
  }
}

/**
 * `consent_required`: consents have to be accepted. At sign-up (`400`) a required consent is
 * missing or outdated; on a request of a signed-in user (`403`) a version's `reaccept_by` date has
 * passed. `consents` lists what to accept: `{ name, version }`. Show the texts
 * (`client.consents.text`) and send the acceptance with `client.consents.accept`.
 */
export class ConsentRequiredError extends BackdError {
  /**
   * @param {ConstructorParameters<typeof BackdError>[0]} init
   */
  constructor(init) {
    super(init)
    /** @type {Array<{ name: string, version: string }>} */
    this.consents = []
    for (const d of init.details ?? []) {
      const name = /^consents\.(.+)$/.exec(d.path)?.[1]
      const version = /version (\S+)$/.exec(d.reason)?.[1]
      if (name && version) this.consents.push({ name, version })
    }
  }
}

/**
 * 400 `id_required`, `invalid_id` or `id_not_allowed`: the `id` of a create is missing, isn't
 * acceptable (`code` says which; the `details` say why) or was sent to a collection that
 * generates its ids.
 */
export class InvalidIdError extends ValidationError {}

/**
 * 403 `org_requires_mfa`: the organization requires a second factor and this session used none.
 * Ask for one (see `mfa_met` of `client.orgs.list()`); leaving the organization still works.
 */
export class OrgRequiresMFAError extends ForbiddenError {}

/**
 * 403 `org_role_requires_mfa`: the action needs a role that requires a second factor and this
 * session used none. `details` names the `role` (and the `team`, for a team role): start the
 * enrollment, or sign in again with a second factor.
 */
export class OrgRoleRequiresMFAError extends ForbiddenError {}

/**
 * 403 `not_team_member`: the create names a team the caller doesn't belong to and holds no team
 * role in (the same answer for an unknown team and one of another organization).
 */
export class NotTeamMemberError extends ForbiddenError {}

/**
 * 400 `team_required` or `team_not_allowed`: the create of a collection with a required team
 * names none, or names one in a collection that has no teams (`code` says which).
 */
export class TeamError extends ValidationError {}

/**
 * 400 `invalid_expand`: the `expand` of a read names a reference the collection doesn't declare,
 * a field the target doesn't have, goes too deep, has too many entries or isn't well formed. The
 * `details` name the part.
 */
export class InvalidExpandError extends ValidationError {}

/**
 * 400 `expand_too_large`: the expansion would return more documents than the server allows
 * (`BACKD_EXPAND_MAX_DOCS`); nothing partial is returned. Ask for fewer references, a smaller
 * `limit` or fewer documents.
 */
export class ExpandTooLargeError extends ValidationError {}

/**
 * 400 `invalid_aggregate`: an aggregation names a field the collection doesn't declare, a bad
 * bucket, metric, `orderBy`, `limit` or `tz`. The `details` name each parameter.
 */
export class InvalidAggregateError extends ValidationError {}

/** 400 `aggregate_not_enabled`: the collection declares no `aggregate:` in its collection.yaml. */
export class AggregateNotEnabledError extends ValidationError {}

/**
 * 422 `aggregate_too_expensive`: the aggregation needed more time, memory or distinct values
 * than allowed. Narrow `where`, group by fewer fields, or ask for an index that starts with a
 * `group_by` field.
 */
export class AggregateTooExpensiveError extends BackdError {}

/** 400 `search_not_enabled`: a `$search` in a collection that declares no text index. */
export class SearchNotEnabledError extends ValidationError {}

/** 403 `not_org_member`: not a member of the organization, or there is no such organization. */
export class NotOrgMemberError extends ForbiddenError {}

/** 404: unknown realm, collection, document, user or session. */
export class NotFoundError extends BackdError {}

/** 409: a unique value already exists (`conflict`, `email_taken`) or concurrent writes (`write_conflict`). */
export class ConflictError extends BackdError {}

/** 409 `id_taken`: a document with the id of a create exists. Nothing was overwritten. */
export class IdTakenError extends ConflictError {}

/** 409 `last_org_manager`: the organization needs at least one member who can manage it. */
export class LastOrgManagerError extends ConflictError {}

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

/** 422 `search_too_expensive`: a search needed more time than allowed; use fewer or rarer words, or narrow `where`. */
export class SearchTooExpensiveError extends BackdError {}

/**
 * 503 `index_building`: a search while the collection's text index is being rebuilt. It is a
 * `RetryableError`: the client retries it after `Retry-After`, and `retryAfter` says how long.
 */
export class IndexBuildingError extends RetryableError {}

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
 * @param {string} code
 * @param {typeof BackdError} fallback
 * @returns {typeof BackdError}
 */
function typedClass(code, fallback) {
  switch (code) {
    case 'consent_required':
      return ConsentRequiredError
    case 'password_expired':
      return PasswordExpiredError
    case 'realtime_not_enabled':
      return RealtimeNotEnabledError
    case 'too_many_subscriptions':
      return TooManySubscriptionsError
    case 'invalid_resume_token':
      return InvalidResumeTokenError
    case 'id_required':
    case 'invalid_id':
    case 'id_not_allowed':
      return InvalidIdError
    case 'id_taken':
      return IdTakenError
    case 'org_requires_mfa':
      return OrgRequiresMFAError
    case 'org_role_requires_mfa':
      return OrgRoleRequiresMFAError
    case 'not_team_member':
      return NotTeamMemberError
    case 'team_required':
    case 'team_not_allowed':
      return TeamError
    case 'not_org_member':
      return NotOrgMemberError
    case 'last_org_manager':
      return LastOrgManagerError
    case 'invalid_aggregate':
      return InvalidAggregateError
    case 'aggregate_not_enabled':
      return AggregateNotEnabledError
    case 'aggregate_too_expensive':
      return AggregateTooExpensiveError
    case 'search_not_enabled':
      return SearchNotEnabledError
    case 'search_too_expensive':
      return SearchTooExpensiveError
    case 'index_building':
      return IndexBuildingError
    case 'invalid_expand':
      return InvalidExpandError
    case 'expand_too_large':
      return ExpandTooLargeError
  }
  return fallback
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
  const code = e.code ?? 'http_' + res.status
  const Typed = typedClass(code, Class)
  return new Typed({
    status: res.status,
    code,
    message: e.message ?? res.statusText ?? 'request failed',
    details: e.details ?? [],
    requestId: e.request_id ?? res.headers.get('X-Request-ID') ?? undefined,
    ...(Typed === RetryableError || Typed.prototype instanceof RetryableError ? { retryAfter } : {}),
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
