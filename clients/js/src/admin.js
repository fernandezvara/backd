import { Job } from './functions.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * A user as the admin API shows it.
 * @typedef {object} AdminUser
 * @property {string} id
 * @property {string} email
 * @property {boolean} email_verified
 * @property {string} locale
 * @property {string[]} roles
 * @property {boolean} disabled
 * @property {string[]} admin_networks  CIDR networks the user's admin requests must come from; empty: no own restriction.
 * @property {string[]} login_networks  CIDR networks the user's login and session must be used from; empty: anywhere.
 * @property {string} created_at
 * @property {string} updated_at
 * @property {string | null} erased_at   When the user was erased; a tombstone keeps only its id (and a placeholder email).
 */

/**
 * @typedef {object} UserPage
 * @property {AdminUser[]} items
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 * @property {string} [next_cursor]   With `has_more`: pass it as `after` for the next page.
 */

/**
 * @typedef {object} Invitation
 * @property {string} id
 * @property {string | null} email    Only this email may use it; null for anyone.
 * @property {string} created_by
 * @property {string} created_at
 * @property {string} expires_at
 */

/**
 * What erasing a user would do, for the collections that declare a policy
 * (`collection.yaml`); the others are only named in `without_policy`.
 * @typedef {object} OwnedReport
 * @property {{ id: string, status: 'active' | 'deactivated' | 'erased' }} user
 * @property {Array<{ database: string, collection: string, action: 'delete' | 'anonymize' | null, owned: number, remove?: string[], replace?: string[], pull?: Record<string, number>, unset?: Record<string, number> }>} collections
 *   `owned` counts the documents the user owns; `pull` and `unset` count, per field, the documents that hold the user.
 * @property {string[]} without_policy  `<database>.<collection>` of the collections an erase leaves alone.
 */

/**
 * @typedef {Invitation & { token: string }} NewInvitation
 * `token` (`bdi_…`) is returned only when the invitation is created.
 */

/**
 * @typedef {Invitation & { sent: true }} SentInvitation
 * An invitation that was emailed: nobody holds its token.
 */

/**
 * An API key as the admin API lists it (never the key itself).
 * @typedef {object} APIKeyInfo
 * @property {string} name
 * @property {'data' | 'admin'} role
 * @property {string} prefix          First characters of the key.
 * @property {string[]} networks      Where it may be used from; empty: anywhere.
 * @property {string} created_at
 * @property {string | null} last_used_at
 * @property {string | null} expires_at
 */

/**
 * @typedef {APIKeyInfo & { key: string }} NewAPIKey
 * `key` (`bdk_…`) is returned only when the key is created.
 */

/**
 * One entry of the realm's audit trail. Never holds secrets or emails.
 * @typedef {object} AuditRecord
 * @property {string} id
 * @property {string} at
 * @property {string} action          Such as `user.create`, `role.add`, `apikey.create`, `admin.login`.
 * @property {string} actor           `user:<id>`, `key:<name>`, `anonymous`, `config:realm.yaml` or `cli:bootstrap`.
 * @property {string | null} target   `user:<id>`, `key:<name>`, `invitation:<id>`, or null.
 * @property {Record<string, unknown>} details
 * @property {string | null} request_id
 * @property {string | null} client_ip
 */

/**
 * A function secret's metadata, as listed: never its value.
 * @typedef {object} SecretInfo
 * @property {string} database     Its database scope; empty means the realm scope.
 * @property {string} name
 * @property {string} created_at
 * @property {string} updated_at
 * @property {string} updated_by   Who last set it, such as `user:<id>` or `key:<name>`.
 */

/**
 * One function call in the invocation history: what happened and the
 * function's own console lines, never its input or output.
 * @typedef {object} InvocationRecord
 * @property {string} id
 * @property {string} at
 * @property {string} function            `<database>/<name>`.
 * @property {string} actor               `user:<id>`, `key:<name>`, `anonymous`, ...
 * @property {string} mode                `sync` or `async`.
 * @property {string} status              `ok`, `function_error`, `timeout`, `memory`, `cpu`, `crash`, `output_too_large`, `busy` or `bundle`.
 * @property {string | null} code         The function's own error code.
 * @property {number} duration_ms
 * @property {string | null} request_id
 * @property {string | null} job_id       The async job this call ran for.
 * @property {string | null} parent_id    The invocation that called this one with `ctx.call`.
 * @property {string | null} origin       `http`, `function`, `cron`, `admin` or `backd:<event>`.
 * @property {{ level: string, line: string }[]} logs
 */

/**
 * @typedef {object} InvocationsPage
 * @property {InvocationRecord[]} items   Newest first.
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 */

/**
 * @typedef {object} AuditPage
 * @property {AuditRecord[]} items    Newest first.
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 */

/**
 * An async or scheduled job as the admin listing shows it: its state and
 * how it ended, never its input or output.
 * @typedef {object} JobSummary
 * @property {string} id                     For cron runs, `cron_<database>_<function>_<yyyymmddhhmm>` (UTC).
 * @property {string} function               `<database>/<name>`.
 * @property {'queued' | 'running' | 'done'} status
 * @property {boolean} scheduled             True for a cron run.
 * @property {number} attempts               More than 1 after a worker was lost mid-run.
 * @property {string} created_at
 * @property {string | null} completed_at
 * @property {{ status: string, code: string | null, duration_ms: number } | null} result   Null until `done`.
 */

/**
 * @typedef {object} JobsPage
 * @property {JobSummary[]} items    Newest first.
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 */

/**
 * The admin API: `client.admin`. Needs a client created with an admin API
 * key, or signed in as a user holding one of the realm's admin roles.
 */
export class Admin {
  /** @param {Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
    /** Users and their roles. */
    this.users = new AdminUsers(this)
    /** Invitations for realms with `signup: invite`. */
    this.invitations = new AdminInvitations(this)
    /** The realm's API keys. */
    this.apiKeys = new AdminAPIKeys(this)
    /** The realm's audit trail (read-only). */
    this.audit = new AdminAudit(this)
    /** The realm's async and scheduled jobs (read-only). */
    this.jobs = new AdminJobs(this)
    /** Function secrets: set and delete their values, list their metadata. */
    this.secrets = new AdminSecrets(this)
    /** The realm's function invocation history (read-only). */
    this.invocations = new AdminInvocations(this)
  }

  /**
   * Runs a function by hand, internal ones included: to re-run a clean-up
   * that failed, or to test a scheduled function. `function` is
   * `<database>/<name>`. With `as` (a user's email) the function runs with
   * that user as `ctx.user`; without it there is no user. Returns the
   * output of a `sync` function, and a `Job` handle for an `async` one,
   * like `db.fn()`. The function's `invoke` rule and `rate_limit` don't
   * apply; every run is audited.
   * @param {string} fn
   * @param {{ input?: unknown, as?: string, idempotencyKey?: string }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<unknown | Job>}
   */
  async invokeFunction(fn, { input, as, idempotencyKey } = {}, opts) {
    const [database, name, ...rest] = fn.split('/')
    if (!database || !name || rest.length > 0) throw new TypeError('invokeFunction: the function must be "<database>/<name>"')
    /** @type {Record<string, unknown>} */
    const body = { input: input === undefined ? null : input }
    if (as !== undefined) body.as = as
    const headers = idempotencyKey === undefined ? undefined : { 'Idempotency-Key': idempotencyKey }
    const { status, data } = await this._request({ method: 'POST', path: ['functions', database, name, 'invoke'], body, headers, ...opts })
    if (status === 202) return new Job(this.client, database, /** @type {import('./functions.js').JobData} */ (data))
    return data
  }

  /**
   * @internal
   * @param {import('./client.js').RequestInit} req
   */
  async _request(req) {
    if (!this.client.hasApiKey && !(await this.client.auth.token())) {
      throw new Error('client.admin needs a client created with an `apiKey` (admin role), or a signed-in user with an admin role')
    }
    return this.client.request({ ...req, path: ['_admin', ...req.path] })
  }
}

class AdminUsers {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * A page of users, sorted by email. `after` is the `next_cursor` of the
   * previous page (not combinable with `skip`).
   * @param {{ limit?: number, skip?: number, after?: string }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<UserPage>}
   */
  async list(params = {}, opts) {
    return (await this.admin._request({ method: 'GET', path: ['users'], query: { limit: params.limit, skip: params.skip, after: params.after }, ...opts })).data
  }

  /**
   * The user with this email (case-insensitive), or null.
   * @param {string} email
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser | null>}
   */
  async find(email, opts) {
    const { data } = await this.admin._request({ method: 'GET', path: ['users'], query: { email }, ...opts })
    return data.items[0] ?? null
  }

  /**
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async get(id, opts) {
    return (await this.admin._request({ method: 'GET', path: ['users', id], ...opts })).data
  }

  /**
   * Creates a user. Without a password they can't sign in until one is set.
   * @param {{ email: string, password?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async create({ email, password }, opts) {
    /** @type {Record<string, string>} */
    const body = { email }
    if (password !== undefined) body.password = password
    return (await this.admin._request({ method: 'POST', path: ['users'], body, ...opts })).data
  }

  /**
   * Changes a user's flags. Disabling ends all their sessions. Emails can't change.
   * @param {string} id
   * @param {{ emailVerified?: boolean, disabled?: boolean }} changes
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async update(id, { emailVerified, disabled }, opts) {
    /** @type {Record<string, boolean>} */
    const body = {}
    if (emailVerified !== undefined) body.email_verified = emailVerified
    if (disabled !== undefined) body.disabled = disabled
    return (await this.admin._request({ method: 'PATCH', path: ['users', id], body, ...opts })).data
  }

  /**
   * Erases a user. **Irreversible.** The user becomes a tombstone at once (the
   * id stays, the email becomes `erased-<id>@erased.invalid`, and their
   * sessions, sign-in methods and email tokens are deleted); a worker then
   * applies the `collection.yaml` policy of every collection that declares one.
   * Resolves with the erase job (`origin: backd:account.erase` in
   * `admin.jobs.list()`); the counts land in the audit trail as `user.erased`.
   * To keep the data, deactivate with `update(id, { disabled: true })`.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ id: string, status: 'queued' | 'running' | 'done' }>}
   */
  async delete(id, opts) {
    return (await this.admin._request({ method: 'DELETE', path: ['users', id], ...opts })).data
  }

  /**
   * Sets a user's password and ends all their sessions.
   * @param {string} id
   * @param {string} password
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async setPassword(id, password, opts) {
    await this.admin._request({ method: 'POST', path: ['users', id, 'password'], body: { password }, ...opts })
  }

  /**
   * What erasing a user would do: counts per collection that declares a policy,
   * and the collections an erase leaves alone. No document content.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<OwnedReport>}
   */
  async owned(id, opts) {
    return (await this.admin._request({ method: 'GET', path: ['users', id, 'owned'], ...opts })).data
  }

  /**
   * Changes a user's email address at once, in a realm with `email`: the new
   * address counts as verified, the user's sessions end, the old address is
   * sent a link to undo the change and the new one is told.
   * @param {string} id
   * @param {string} email
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async changeEmail(id, email, opts) {
    return (await this.admin._request({ method: 'POST', path: ['users', id, 'email'], body: { email }, ...opts })).data
  }

  /**
   * Assigns a role declared in realm.yaml.
   * @param {string} id
   * @param {string} role
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async addRole(id, role, opts) {
    return (await this.admin._request({ method: 'PUT', path: ['users', id, 'roles', role], ...opts })).data
  }

  /**
   * Takes a role away.
   * @param {string} id
   * @param {string} role
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async removeRole(id, role, opts) {
    return (await this.admin._request({ method: 'DELETE', path: ['users', id, 'roles', role], ...opts })).data
  }

  /**
   * Replaces the user's network restrictions (IP addresses or CIDR
   * networks); empty lists remove them. Settings in realm.yaml win at the
   * next startup.
   * @param {string} id
   * @param {{ adminNetworks?: string[], loginNetworks?: string[] }} networks
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminUser>}
   */
  async setNetworks(id, { adminNetworks = [], loginNetworks = [] }, opts) {
    const body = { admin_networks: adminNetworks, login_networks: loginNetworks }
    return (await this.admin._request({ method: 'PUT', path: ['users', id, 'networks'], body, ...opts })).data
  }
}

class AdminAPIKeys {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * The realm's API keys, by name; never the keys themselves.
   * @param {RequestOptions} [opts]
   * @returns {Promise<APIKeyInfo[]>}
   */
  async list(opts) {
    return (await this.admin._request({ method: 'GET', path: ['apikeys'], ...opts })).data.items
  }

  /**
   * Creates a key; store its `key` now, it is never shown again.
   * @param {{ name: string, role?: 'data' | 'admin', expiresIn?: string, networks?: string[] }} input
   *   `role` defaults to data; `expiresIn`: days (`90d`) or Go durations (`12h`).
   * @param {RequestOptions} [opts]
   * @returns {Promise<NewAPIKey>}
   */
  async create({ name, role, expiresIn, networks }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { name }
    if (role !== undefined) body.role = role
    if (expiresIn !== undefined) body.expires_in = expiresIn
    if (networks !== undefined) body.networks = networks
    return (await this.admin._request({ method: 'POST', path: ['apikeys'], body, ...opts })).data
  }

  /**
   * Revokes a key: it stops working at once.
   * @param {string} name
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revoke(name, opts) {
    await this.admin._request({ method: 'DELETE', path: ['apikeys', name], ...opts })
  }
}

class AdminAudit {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * A page of the audit trail, newest first. `since` and `until` are dates
   * or RFC 3339 strings.
   * @param {{ action?: string, actor?: string, target?: string, since?: Date | string, until?: Date | string, limit?: number, skip?: number }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<AuditPage>}
   */
  async list(params = {}, opts) {
    const time = (/** @type {Date | string | undefined} */ t) => (t instanceof Date ? t.toISOString() : t)
    const query = {
      action: params.action, actor: params.actor, target: params.target,
      since: time(params.since), until: time(params.until), limit: params.limit, skip: params.skip,
    }
    return (await this.admin._request({ method: 'GET', path: ['audit'], query, ...opts })).data
  }
}

class AdminSecrets {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * The realm's secrets: scope, name and who last changed them. Values are
   * write-only and never returned.
   * @param {RequestOptions} [opts]
   * @returns {Promise<SecretInfo[]>}
   */
  async list(opts) {
    return (await this.admin._request({ method: 'GET', path: ['secrets'], ...opts })).data.items
  }

  /**
   * Creates a secret or replaces its value; functions read it as
   * `ctx.secrets` within about a minute. Without `database` it is the
   * realm's secret (`realm.NAME` in function.yaml); with it, that
   * database's (`NAME`).
   * @param {string} name   Upper-case letters, digits and `_`.
   * @param {string} value
   * @param {{ database?: string }} [scope]
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async set(name, value, { database } = {}, opts) {
    /** @type {Record<string, string>} */
    const body = { value }
    if (database) body.database = database
    await this.admin._request({ method: 'PUT', path: ['secrets', name], body, ...opts })
  }

  /**
   * Removes a secret's value: a function that declares it answers
   * `secret_missing` again.
   * @param {string} name
   * @param {{ database?: string }} [scope]
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(name, { database } = {}, opts) {
    await this.admin._request({ method: 'DELETE', path: ['secrets', name], query: { database }, ...opts })
  }
}

class AdminInvocations {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * A page of the function invocation history, newest first. `function` is
   * `<database>/<name>`; `since` and `until` are dates or RFC 3339 strings.
   * @param {{ function?: string, requestId?: string, since?: Date | string, until?: Date | string, limit?: number, skip?: number }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<InvocationsPage>}
   */
  async list(params = {}, opts) {
    const time = (/** @type {Date | string | undefined} */ t) => (t instanceof Date ? t.toISOString() : t)
    const query = {
      function: params.function, request_id: params.requestId,
      since: time(params.since), until: time(params.until), limit: params.limit, skip: params.skip,
    }
    return (await this.admin._request({ method: 'GET', path: ['invocations'], query, ...opts })).data
  }
}

class AdminJobs {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * A page of jobs, newest first: their state and outcome, never their
   * input or output (read one job in full with `Job.status()`/`wait()`).
   * `function` is `<database>/<name>`; `since` and `until` are dates or
   * RFC 3339 strings, on the job's creation time.
   * @param {{ function?: string, status?: 'queued' | 'running' | 'done', scheduled?: boolean, since?: Date | string, until?: Date | string, limit?: number, skip?: number }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobsPage>}
   */
  async list(params = {}, opts) {
    const time = (/** @type {Date | string | undefined} */ t) => (t instanceof Date ? t.toISOString() : t)
    const query = {
      function: params.function, status: params.status, scheduled: params.scheduled,
      since: time(params.since), until: time(params.until), limit: params.limit, skip: params.skip,
    }
    return (await this.admin._request({ method: 'GET', path: ['jobs'], query, ...opts })).data
  }
}

class AdminInvitations {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * Creates an invitation; deliver its `token` to the invitee.
   * @param {{ email?: string, expiresIn?: string }} [input] `expiresIn`: days (`7d`) or Go durations (`12h`).
   * @param {RequestOptions} [opts]
   * @returns {Promise<NewInvitation>}
   */
  async create({ email, expiresIn } = {}, opts) {
    /** @type {Record<string, string>} */
    const body = {}
    if (email !== undefined) body.email = email
    if (expiresIn !== undefined) body.expires_in = expiresIn
    return (await this.admin._request({ method: 'POST', path: ['invitations'], body, ...opts })).data
  }

  /**
   * Creates an invitation and has `backd` email it to `email` (needs `email`
   * in the realm's `realm.yaml`): the link opens a page where the person
   * chooses a password, or your own page with `email.links.invitation` (see
   * `auth.acceptInvitation`). There is no token to deliver. `redirectTo` is
   * where the page after accepting may send them (within
   * `email.allowed_redirects`); `locale` is the language of the email.
   * @param {{ email: string, expiresIn?: string, redirectTo?: string, locale?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<SentInvitation>}
   */
  async send({ email, expiresIn, redirectTo, locale }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { email, send: true }
    if (expiresIn !== undefined) body.expires_in = expiresIn
    if (redirectTo !== undefined) body.redirect_to = redirectTo
    if (locale !== undefined) body.locale = locale
    return (await this.admin._request({ method: 'POST', path: ['invitations'], body, ...opts })).data
  }

  /**
   * Unused, unexpired invitations, newest first. Tokens are never listed.
   * @param {RequestOptions} [opts]
   * @returns {Promise<Invitation[]>}
   */
  async list(opts) {
    return (await this.admin._request({ method: 'GET', path: ['invitations'], ...opts })).data.items
  }

  /**
   * Revokes an invitation.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revoke(id, opts) {
    await this.admin._request({ method: 'DELETE', path: ['invitations', id], ...opts })
  }
}
