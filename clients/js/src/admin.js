import { Collection } from './data.js'
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
 * What the signed-in administrator may do (`GET /_admin/whoami`).
 * @typedef {object} AdminAccess
 * @property {'full' | 'read' | 'custom'} level
 * @property {string[]} write   Areas it may change.
 * @property {string[]} read    Areas it may read, what `read_access` grants included.
 * @property {{ users: boolean, data: boolean }} read_access
 * @property {{ id: string, email: string, roles: string[] }} [user]
 * @property {string} [key]     The admin API key's name.
 */

/**
 * The realm's configuration as this instance runs it (`GET /_admin/config`).
 * @typedef {object} AdminConfig
 * @property {string} realm
 * @property {string} fingerprint
 * @property {string} file
 * @property {Record<string, any>} settings
 * @property {Record<string, any>} databases
 * @property {Record<string, any>} templates
 * @property {{ code: string, message: string }[]} warnings
 */

/**
 * A session of a user: never its token.
 * @typedef {object} UserSession
 * @property {string} id
 * @property {string} created_at
 * @property {string} last_used_at
 * @property {string} expires_at
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
 * @property {string[]} scopes        What it reaches (`read:blog/posts`, `call:main/export`…); empty: everything.
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
 * @property {string | null} origin       `http`, `function`, `cron`, `admin`, `on_complete:<database>/<name>` or `backd:<event>`.
 * @property {{ level: string, line: string }[]} logs
 * @property {import('./functions.js').Step[]} steps   What the call reported with `ctx.step()`, closed when it ended.
 * @property {number} steps_omitted
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
 * @property {string} origin                 `http`, `function`, `cron`, `admin`, `on_complete:<database>/<name>`, `backd:email.<kind>` or `function:<database>/<name>`.
 * @property {string | null} email_kind      The kind of an email job, never its recipients.
 * @property {string | null} rerun_of        The finished job an administrator re-ran to make this one.
 * @property {{ step: number, name: string, status: string, current: number, total: number | null, message: string | null, updated_at: string } | null} progress   The current step; the job's `get()` has every step.
 * @property {import('./functions.js').Step[]} [steps]   Only from `jobs.get()`: every step of the running or last attempt.
 * @property {number} [steps_omitted]
 * @property {number} attempts               More than 1 after a worker was lost mid-run.
 * @property {string} created_at
 * @property {string | null} completed_at
 * @property {{ status: string, code: string | null, duration_ms: number } | null} result   Null until `done`; `status` is `cancelled` for a cancelled job and `skipped` for a scheduled run that did not happen because the previous one had not finished.
 */

/**
 * A scheduled function and whether its schedule is paused.
 * @typedef {object} Schedule
 * @property {string} function        `<database>/<name>`.
 * @property {string} schedule        The cron expression.
 * @property {string} timezone        The IANA time zone it is read in (`UTC` by default).
 * @property {'allow' | 'skip'} overlap
 * @property {boolean} paused
 * @property {string | null} changed_at   When it was last paused or resumed; null if never.
 * @property {string | null} changed_by   The actor that did, such as `key:ops`; null if never.
 */

/**
 * What a schema check found in one collection (without the documents).
 * @typedef {object} CheckReportSummary
 * @property {string} database
 * @property {string} collection
 * @property {string} job_id          The check job that made it.
 * @property {string} started_at
 * @property {string} finished_at
 * @property {number} scanned         How many documents it read.
 * @property {number} invalid         How many did not match the schema (up to `limit`).
 * @property {boolean} complete       False when the scan stopped before the end.
 * @property {'limit' | 'time' | null} stopped_by
 * @property {number} limit
 * @property {string} schema_hash     Identifies the schema the documents were checked against.
 */

/**
 * A collection's latest schema check report, with the documents that failed.
 * @typedef {CheckReportSummary & { documents: { id: string, deleted: boolean, problems: { path: string, reason: string }[], more_problems: number }[] }} CheckReport
 */

/**
 * @typedef {object} DataCheckStarted
 * @property {string} id                          The job's id.
 * @property {string} status
 * @property {string} scope                       `<database>/<collection>`, `<database>` or `*`.
 * @property {string[]} collections               `<database>/<collection>` of each collection it reads.
 * @property {number} limit
 * @property {number | null} estimated_documents  About how many documents it reads; null when unknown.
 * @property {string} created_at
 */

/**
 * @typedef {object} DataChecksList
 * @property {JobSummary | null} running   The check queued or running now (`progress` is the collection it is at).
 * @property {{ database: string, collection: string, report: CheckReportSummary | null }[]} items   Every collection with its latest report, or null when never checked.
 */

/**
 * What `admin.storage.check()` found in the realm's object storage.
 * @typedef {object} StorageCheck
 * @property {boolean} ok                      False when a step failed.
 * @property {'aws' | 'minio' | 'r2' | 'digitalocean'} provider
 * @property {string} endpoint                 What backd connects to.
 * @property {string | null} public_endpoint   The address signed links use, when it differs.
 * @property {string} bucket
 * @property {string} prefix
 * @property {{ name: string, level: 'ok' | 'warn' | 'fail' | 'skipped', detail: string }[]} steps
 * @property {'verified' | 'ignored' | 'not tested'} checksum_sha256   What the storage does with a signed `x-amz-checksum-sha256`.
 * @property {string} encryption               The bucket's default encryption, `none` or `unknown`.
 * @property {{ origins: string[], methods: string[], headers: string[] }[]} cors
 * @property {string} link_host                The host a signed link is for.
 */

/**
 * What `admin.checkConfig()` found.
 * @typedef {object} ConfigCheck
 * @property {boolean} ok         False when the configuration with the drafts would not load.
 * @property {string} realm
 * @property {number} checked     How many files of the realm were loaded and validated, drafts included.
 * @property {string[]} changed   The draft files.
 * @property {{ file?: string, message: string }[]} problems
 */

/**
 * What `admin.storage.status()` answers: how the realm's storage is configured (the keys by
 * name, never their values), whether it works, and what the documents hold in it.
 * @typedef {object} StorageStatus
 * @property {boolean} configured   False when the realm has no `storage:`; nothing else is set then.
 * @property {string} [provider]
 * @property {string} [endpoint]
 * @property {string | null} [public_endpoint]
 * @property {string} [bucket]
 * @property {string} [prefix]
 * @property {{ ok: boolean, error?: string }} [keys]        Whether the access keys are set and readable.
 * @property {{ ok: boolean, error?: string }} [reachable]   Whether the bucket answers.
 * @property {{ bytes: number, files: number, users: { user_id: string, bytes: number, files: number }[] }} [usage]
 *   What documents reference: the realm's totals and the users holding the most.
 * @property {{ queued: number, retrying: number, oldest: string | null }} [deletions]   Objects waiting to be deleted.
 * @property {{ realm: number | null, user: number | null }} [quota]   The limits usage is held to (`storage.quota`), in bytes; null: none.
 * @property {number} [stale_uploads]   Uploads left unfinished past their time.
 */

/**
 * What `admin.storage.reconcile()` found.
 * @typedef {object} StorageReconcile
 * @property {boolean} delete
 * @property {number} scanned
 * @property {number} referenced
 * @property {number} skipped_recent
 * @property {number} skipped_in_journal
 * @property {number} ignored
 * @property {{ key: string, database: string, collection: string, file_id: string, size: number, last_modified: string }[]} orphans
 * @property {number} orphan_bytes
 * @property {number} deleted
 * @property {string[]} failed
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
    /** The realm's object storage (its files): check that it works. */
    this.storage = new AdminStorage(this)
    /** Schema checks: which stored documents no longer match their collection's schema. */
    this.dataChecks = new AdminDataChecks(this)
    /** The realm's schedules: list them, pause and resume them. */
    this.schedules = new AdminSchedules(this)
    /** Function secrets: set and delete their values, list their metadata. */
    this.secrets = new AdminSecrets(this)
    /** The realm's function invocation history (read-only). */
    this.invocations = new AdminInvocations(this)
  }

  /**
   * A collection through the admin data route (`/_admin/data/...`): the
   * same operations as `client.db(...).collection(...)` (list, get, create,
   * replace, patch, delete with `purge`, restore, `deleted` and `where`),
   * served past the collection's rules to an administrator with the `data`
   * area (a read-only administrator reads only with `admin.read_access.data`).
   * The schema still validates every write, `ifMatch` works, and writes are
   * audited. A document created here has no owner.
   * @template {object} [T=Record<string, any>]
   * @param {string} database
   * @param {string} collection
   * @returns {Collection<T>}
   */
  data(database, collection) {
    const c = /** @type {Collection<T>} */ (new Collection(this.client, database, collection))
    c.path = ['_admin', 'data', database, collection]
    return c
  }

  /**
   * What the signed-in administrator may do: the level and the areas it may
   * change and read. A client shows only what this allows; the server
   * enforces it anyway.
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminAccess>}
   */
  async whoami(opts) {
    return (await this._request({ method: 'GET', path: ['whoami'], ...opts })).data
  }

  /**
   * The realm's configuration as this instance runs it, read-only.
   * @param {RequestOptions} [opts]
   * @returns {Promise<AdminConfig>}
   */
  async config(opts) {
    return (await this._request({ method: 'GET', path: ['config'], ...opts })).data
  }

  /**
   * Checks a change to the configuration without applying it: loads the realm's whole
   * configuration as startup does, with `files` (path in the realm → content, or null to delete)
   * in place of the ones on disk, and says what it found. Every file of the realm is validated,
   * not only these. Nothing on the server changes. Needs the `config` area.
   * @param {Record<string, string | null>} files   e.g. `{ 'main/notes/rules.yaml': 'read: user != nil\n' }`
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConfigCheck>}
   */
  async checkConfig(files, opts) {
    return (await this._request({ method: 'POST', path: ['config', 'check'], body: { files }, ...opts })).data
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
   * With `q`, only users whose email contains it (case-insensitive).
   * @param {{ limit?: number, skip?: number, after?: string, q?: string }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<UserPage>}
   */
  async list(params = {}, opts) {
    return (await this.admin._request({ method: 'GET', path: ['users'], query: { limit: params.limit, skip: params.skip, after: params.after, q: params.q }, ...opts })).data
  }

  /**
   * The user's unexpired sessions, newest first.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<UserSession[]>}
   */
  async sessions(id, opts) {
    return (await this.admin._request({ method: 'GET', path: ['users', id, 'sessions'], ...opts })).data.items
  }

  /**
   * Ends one of a user's sessions: its token stops working at once.
   * @param {string} id
   * @param {string} sessionId
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revokeSession(id, sessionId, opts) {
    await this.admin._request({ method: 'DELETE', path: ['users', id, 'sessions', sessionId], ...opts })
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
   * @param {{ name: string, role?: 'data' | 'admin', expiresIn?: string, networks?: string[], scopes?: string[] }} input
   *   `role` defaults to data; `expiresIn`: days (`90d`) or Go durations (`12h`). `scopes` limit what a
   *   data key reaches: `read`, `write` or `call`, optionally followed by `:<database>` or
   *   `:<database>/<collection or function>`; none means everything.
   * @param {RequestOptions} [opts]
   * @returns {Promise<NewAPIKey>}
   */
  async create({ name, role, expiresIn, networks, scopes }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { name }
    if (role !== undefined) body.role = role
    if (expiresIn !== undefined) body.expires_in = expiresIn
    if (networks !== undefined) body.networks = networks
    if (scopes !== undefined) body.scopes = scopes
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
   * `function` is `<database>/<name>`; `origin` is exact (such as
   * `function:main/ship`); `since` and `until` are dates or RFC 3339
   * strings, on the job's creation time.
   * @param {{ function?: string, status?: 'queued' | 'running' | 'done', origin?: string, scheduled?: boolean, since?: Date | string, until?: Date | string, limit?: number, skip?: number }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobsPage>}
   */
  async list(params = {}, opts) {
    const time = (/** @type {Date | string | undefined} */ t) => (t instanceof Date ? t.toISOString() : t)
    const query = {
      function: params.function, status: params.status, origin: params.origin, scheduled: params.scheduled,
      since: time(params.since), until: time(params.until), limit: params.limit, skip: params.skip,
    }
    return (await this.admin._request({ method: 'GET', path: ['jobs'], query, ...opts })).data
  }

  /**
   * One job as the listing shows it, with every step its running or last attempt
   * reported (`steps`; `steps_omitted` counts those dropped from the middle past
   * 100). Rejects with a `NotFoundError` when there is no such job.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobSummary>}
   */
  async get(id, opts) {
    return (await this.admin._request({ method: 'GET', path: ['jobs', id], ...opts })).data
  }

  /**
   * Cancels a queued, retry-waiting or running function job: it is `done` at
   * once with the result `cancelled`, and a worker running it stops the run
   * (what the function already did stays done). Rejects with a
   * `ConflictError` when it has already finished or isn't a function's job.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobSummary>}
   */
  async cancel(id, opts) {
    return (await this.admin._request({ method: 'POST', path: ['jobs', id, 'cancel'], ...opts })).data
  }

  /**
   * Queues a finished function job again (a cancelled one included) as a new
   * job with the same function, input and caller; the original keeps its
   * result. Resolves with the new job (`rerun_of` names the original).
   * Rejects with a `ConflictError` while the job hasn't finished, or when it
   * isn't a function's job or its function no longer exists.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<JobSummary>}
   */
  async rerun(id, opts) {
    return (await this.admin._request({ method: 'POST', path: ['jobs', id, 'rerun'], ...opts })).data
  }
}

class AdminStorage {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * Verifies the realm's storage the way files will use it (credentials, put/get/head/delete on
   * the prefix, the signed SHA-256, signed links, CORS and encryption where the provider exposes
   * them) with a few small objects it deletes again. Resolves with the report whatever it found
   * (`ok` is false when a step failed). Rejects with a `NotFoundError` when the realm has no
   * `storage:`, and with a `BackdError` with code `storage_unavailable` when its keys aren't set.
   * @param {RequestOptions} [opts]
   * @returns {Promise<StorageCheck>}
   */
  async check(opts) {
    return (await this.admin._request({ method: 'POST', path: ['storage', 'check'], ...opts })).data
  }

  /**
   * How the storage is configured, whether it works, and what the documents hold in it: the
   * realm's totals of bytes and files and the users holding the most, what waits to be deleted
   * and the uploads left unfinished. A realm without `storage:` answers `{ configured: false }`.
   * @param {RequestOptions} [opts]
   * @returns {Promise<StorageStatus>}
   */
  async status(opts) {
    return (await this.admin._request({ method: 'GET', path: ['storage'], ...opts })).data
  }

  /**
   * Lists the realm's file objects and reports those no document references (for use after
   * restoring a backup or removing a file field); with `delete: true` removes exactly those. It
   * skips anything younger than 24 hours or with an upload record still open. Needs write access
   * to the `config` area.
   * @param {{ delete?: boolean } & RequestOptions} [opts]
   * @returns {Promise<StorageReconcile>}
   */
  async reconcile({ delete: remove = false, ...opts } = {}) {
    return (await this.admin._request({ method: 'POST', path: ['storage', 'reconcile'], body: { delete: remove }, ...opts })).data
  }

  /**
   * Starts the job that makes again, on a worker, the image versions of a file field that no
   * longer match what it declares (a changed declaration, copies that failed, versions added
   * later); copies a function generated with its own parameters are left alone. `missingOnly`
   * makes only the copies that don't exist; `rate` caps the files a second (default 10). Follow
   * it with `admin.jobs`; one runs per field at a time (`regenerate_running`, 409). Needs write
   * access to the `config` area.
   * @param {{ database: string, collection: string, field: string, version?: string, missingOnly?: boolean, rate?: number } & RequestOptions} params
   * @returns {Promise<{ id: string, status: string, database: string, collection: string, field: string, version: string | null, missing_only: boolean, created_at: string }>}
   */
  async regenerateVersions({ database, collection, field, version, missingOnly, rate, ...opts }) {
    const body = { database, collection, field, version, missing_only: missingOnly, rate }
    return (await this.admin._request({ method: 'POST', path: ['files', 'versions', 'regenerate'], body, ...opts })).data
  }
}

class AdminDataChecks {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * Starts a check of the documents that no longer match their schema:
   * `{ database, collection }` for one collection, `{ database }` for a database, nothing for
   * every collection of the realm; `limit` (default 100, at most 1000) is how many invalid
   * documents to list per collection. **It reads every stored document, so on a big
   * collection it takes a long time and loads MongoDB**: `estimated_documents` in the answer
   * is how many, for a warning. It runs as a job on a worker: follow it with `wait()`.
   * Rejects with a `ConflictError` (code `check_running`, the running job's id in
   * `details[0].reason`) while another check is queued or running in the realm.
   * @param {{ database?: string, collection?: string, limit?: number }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<DataCheckStarted>}
   */
  async start(params = {}, opts) {
    return (await this.admin._request({ method: 'POST', path: ['data-checks'], body: params, ...opts })).data
  }

  /**
   * The check running now (or null) and every collection with its latest report
   * summary (null when never checked).
   * @param {RequestOptions} [opts]
   * @returns {Promise<DataChecksList>}
   */
  async list(opts) {
    return (await this.admin._request({ method: 'GET', path: ['data-checks'], ...opts })).data
  }

  /**
   * A collection's latest report in full. Rejects with a `NotFoundError` when it was never checked.
   * @param {string} database
   * @param {string} collection
   * @param {RequestOptions} [opts]
   * @returns {Promise<CheckReport>}
   */
  async get(database, collection, opts) {
    return (await this.admin._request({ method: 'GET', path: ['data-checks', database, collection], ...opts })).data
  }

  /**
   * Waits for a started check to finish and returns the report of each collection it read.
   * `onProgress(job)` is called after every poll with the job, whose `progress` is the
   * step it is at (`name`, `current` percent, `message`). Throws when the check was
   * cancelled or failed.
   * @param {DataCheckStarted | string} started   What `start()` returned, or the job's id.
   * @param {RequestOptions & { pollIntervalMs?: number, onProgress?: (job: JobSummary) => void }} [opts]
   * @returns {Promise<CheckReport[]>}
   */
  async wait(started, opts = {}) {
    const { pollIntervalMs = 2000, onProgress, ...rest } = opts
    const id = typeof started === 'string' ? started : started.id
    for (;;) {
      const job = await this.admin.jobs.get(id, rest)
      onProgress?.(job)
      if (job.status === 'done') {
        if (job.result?.status !== 'ok') throw new Error(`the schema check ${id} ended without a report: ${job.result?.status ?? 'no result'}`)
        break
      }
      await new Promise((resolve) => setTimeout(resolve, pollIntervalMs))
    }
    const collections = typeof started === 'string' ? (await this.list(rest)).items.filter((i) => i.report?.job_id === id).map((i) => `${i.database}/${i.collection}`) : started.collections
    return Promise.all(collections.map((key) => this.get(key.slice(0, key.indexOf('/')), key.slice(key.indexOf('/') + 1), rest)))
  }
}

class AdminSchedules {
  /** @param {Admin} admin */
  constructor(admin) {
    /** @internal */
    this.admin = admin
  }

  /**
   * Every scheduled function of the realm with whether it is paused, sorted
   * by function.
   * @param {RequestOptions} [opts]
   * @returns {Promise<Schedule[]>}
   */
  async list(opts) {
    return (await this.admin._request({ method: 'GET', path: ['schedules'], ...opts })).data.items
  }

  /**
   * Stops a function's schedule creating runs until it is resumed (runs
   * already queued or running are not touched). The state survives restarts.
   * `fn` is `<database>/<name>`. Rejects with a `ConflictError` for a function
   * with no schedule and a `NotFoundError` for an unknown one.
   * @param {string} fn
   * @param {RequestOptions} [opts]
   * @returns {Promise<Schedule>}
   */
  async pause(fn, opts) {
    return this.#set(fn, 'pause', opts)
  }

  /**
   * Lets a paused schedule create runs again; the runs due while it was
   * paused are not made up.
   * @param {string} fn
   * @param {RequestOptions} [opts]
   * @returns {Promise<Schedule>}
   */
  async resume(fn, opts) {
    return this.#set(fn, 'resume', opts)
  }

  /**
   * @param {string} fn
   * @param {'pause' | 'resume'} op
   * @param {RequestOptions | undefined} opts
   * @returns {Promise<Schedule>}
   */
  async #set(fn, op, opts) {
    const [database, name, ...rest] = fn.split('/')
    if (!database || !name || rest.length > 0) throw new TypeError(`schedules.${op}: the function must be "<database>/<name>"`)
    return (await this.admin._request({ method: 'POST', path: ['functions', database, name, op], ...opts })).data
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
