// @backd/functions-testing: a fake `ctx` for `deno test`, so a function's
// own logic (validation, error codes, what it writes) can be tested without
// a running backd, an executor or a network. `ctx.db`/`ctx.admin.db` are
// backed by an in-memory MemoryStore, shaped like the real JavaScript
// client (clients/js): `.collection(name).list/iterate/get/create/replace/
// patch/delete`, and throw the same error classes on 404 and a failed
// `ifMatch` (imported from the client, not reimplemented).
//
// This is a test double, not a reimplementation of backd's query language:
// `where` supports equality and $eq/$ne/$gt/$gte/$lt/$lte/$in/$nin/
// $contains/$icontains/$exists on top-level or dotted fields, plus a
// top-level $or/$and of such objects (not nested further). Access rules
// are not evaluated at all — ctx.db here is always full access, as if
// admin: true — because the point is testing the function's own code, not
// backd's rule engine (that's what the real integration tests are for).
import { BackdError, NotFoundError, VersionMismatchError } from '../../js/src/errors.js'

let seq = 0
/** Deterministic, distinct ids across a test run (resets with resetIds()). */
function nextId() {
  seq += 1
  return `test${String(seq).padStart(8, '0')}`
}

/** Resets the id counter; call between tests that assert on ids. */
export function resetIds() {
  seq = 0
}

function now() {
  return new Date().toISOString()
}

function get(doc, path) {
  return path.split('.').reduce((v, k) => (v == null ? undefined : v[k]), doc)
}

/** @param {string | number} ifMatch */
function ifMatchWant(ifMatch) {
  return typeof ifMatch === 'number' ? String(ifMatch) : String(ifMatch).replace(/^"|"$/g, '')
}

function checkIfMatch(doc, ifMatch) {
  if (ifMatch === undefined) return
  const want = ifMatchWant(ifMatch)
  if (want === '*') return
  if (String(doc._meta.version) !== want) {
    throw new VersionMismatchError({ status: 412, code: 'version_mismatch', message: `If-Match ${ifMatch} doesn't match the current version` })
  }
}

const ops = {
  $eq: (a, b) => a === b,
  $ne: (a, b) => a !== b,
  $gt: (a, b) => a > b,
  $gte: (a, b) => a >= b,
  $lt: (a, b) => a < b,
  $lte: (a, b) => a <= b,
  $in: (a, b) => Array.isArray(b) && b.includes(a),
  $nin: (a, b) => Array.isArray(b) && !b.includes(a),
  $contains: (a, b) => typeof a === 'string' && a.includes(b),
  $icontains: (a, b) => typeof a === 'string' && a.toLowerCase().includes(String(b).toLowerCase()),
  $exists: (a, b) => (a !== undefined) === b,
}

function matchField(value, condition) {
  if (condition === null || typeof condition !== 'object' || Array.isArray(condition)) {
    return value === condition
  }
  return Object.entries(condition).every(([op, want]) => {
    const fn = ops[op]
    if (!fn) throw new TypeError(`@backd/functions-testing: unsupported where operator ${op}`)
    return fn(value, want)
  })
}

/** @param {Record<string, any> | undefined} where */
function matchWhere(where) {
  if (!where) return () => true
  return (doc) => matchOne(doc, where)
}

function matchOne(doc, where) {
  return Object.entries(where).every(([key, condition]) => {
    if (key === '$or') return condition.some((w) => matchOne(doc, w))
    if (key === '$and') return condition.every((w) => matchOne(doc, w))
    return matchField(get(doc, key), condition)
  })
}

function sortBy(items, orderBy) {
  const fields = Array.isArray(orderBy) ? orderBy : String(orderBy).split(',')
  const sorted = [...items]
  sorted.sort((a, b) => {
    for (let f of fields) {
      f = f.trim()
      const desc = f.startsWith('-')
      if (desc) f = f.slice(1)
      const av = get(a, f)
      const bv = get(b, f)
      if (av === bv) continue
      const cmp = av < bv ? -1 : 1
      return desc ? -cmp : cmp
    }
    return 0
  })
  return sorted
}

/** An in-memory store shared by every ctx.db(...) call in one test. */
export class MemoryStore {
  constructor() {
    /** @type {Map<string, Map<string, any>>} */
    this.byCollection = new Map()
  }

  /** @private */
  collection(database, name) {
    const key = `${database}/${name}`
    let c = this.byCollection.get(key)
    if (!c) {
      c = new Map()
      this.byCollection.set(key, c)
    }
    return c
  }

  /**
   * Seeds a collection with documents; missing `id`/`_meta` are filled in.
   * @param {string} database
   * @param {string} name
   * @param {Record<string, any>[]} docs
   */
  seed(database, name, docs) {
    const c = this.collection(database, name)
    for (const doc of docs) {
      const id = doc.id ?? nextId()
      c.set(id, { ...doc, id, _meta: { version: 1, created_at: now(), updated_at: now(), owner: null, ...doc._meta } })
    }
    return this
  }

  /**
   * All documents currently in a collection, for assertions after calling
   * the function under test.
   * @param {string} database
   * @param {string} name
   * @returns {Record<string, any>[]}
   */
  all(database, name) {
    return [...this.collection(database, name).values()]
  }
}

class FakeCollection {
  constructor(store, database, name, actor) {
    this.map = store.collection(database, name)
    this.actor = actor ?? null
  }

  /**
   * The fake's `after` is the offset of the next page (its `next_cursor`):
   * enough to exercise a function that pages with cursors, not the real
   * cursor's position semantics.
   * @returns {Promise<{ items: any[], limit: number, skip: number, has_more: boolean, next_cursor?: string, total?: number }>}
   */
  async list({ where, orderBy, limit = 20, skip = 0, after, count } = {}) {
    if (after !== undefined) {
      if (skip) throw new Error('after can\'t be combined with skip')
      skip = Number(after)
      if (!Number.isInteger(skip) || skip < 0) throw new Error('after is not a cursor of this fake: use the next_cursor of a previous page')
    }
    let items = [...this.map.values()].filter(matchWhere(where))
    if (orderBy) items = sortBy(items, orderBy)
    const page = items.slice(skip, skip + limit)
    /** @type {{ items: any[], limit: number, skip: number, has_more: boolean, next_cursor?: string, total?: number }} */
    const out = { items: page, limit, skip, has_more: skip + limit < items.length }
    if (out.has_more) out.next_cursor = String(skip + limit)
    if (count) out.total = items.length
    return out
  }

  async *iterate(params = {}) {
    const limit = params.limit ?? 100
    for (let skip = 0; ; skip += limit) {
      const page = await this.list({ ...params, limit, skip })
      yield* page.items
      if (!page.has_more) return
    }
  }

  async get(id) {
    const doc = this.map.get(id)
    if (!doc) throw new NotFoundError({ status: 404, code: 'not_found', message: `document not found: ${id}` })
    return doc
  }

  async create(fields) {
    const id = nextId()
    const doc = { ...fields, id, _meta: { version: 1, created_at: now(), updated_at: now(), owner: this.actor } }
    this.map.set(id, doc)
    return doc
  }

  async replace(id, fields, opts = {}) {
    const doc = await this.get(id)
    checkIfMatch(doc, opts.ifMatch)
    const next = { ...fields, id, _meta: { ...doc._meta, version: doc._meta.version + 1, updated_at: now() } }
    this.map.set(id, next)
    return next
  }

  async patch(id, patch, opts = {}) {
    const doc = await this.get(id)
    checkIfMatch(doc, opts.ifMatch)
    const next = mergePatch({ ...doc }, patch)
    next._meta = { ...doc._meta, version: doc._meta.version + 1, updated_at: now() }
    this.map.set(id, next)
    return next
  }

  async delete(id, opts = {}) {
    const doc = await this.get(id)
    checkIfMatch(doc, opts.ifMatch)
    this.map.delete(id)
  }
}

function mergePatch(target, patch) {
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) {
      delete target[k]
    } else if (typeof v === 'object' && !Array.isArray(v) && typeof target[k] === 'object' && target[k] !== null && !Array.isArray(target[k])) {
      target[k] = mergePatch({ ...target[k] }, v)
    } else {
      target[k] = v
    }
  }
  return target
}

/** ctx.db(name) / ctx.admin.db(name): a database of the fake client. */
class FakeDatabase {
  constructor(store, name, actor) {
    this.store = store
    this.name = name
    this.actor = actor
  }

  collection(name) {
    return new FakeCollection(this.store, this.name, name, this.actor)
  }
}

/**
 * A function's error, as `ctx.error(status, code, message, details)`
 * returns it: throw it to answer the caller with that status and code,
 * exactly like the real runner.
 */
export class FunctionError extends Error {
  constructor(status, code, message, details) {
    super(message)
    this.name = 'FunctionError'
    this.status = status
    this.code = code
    this.details = details
  }
}

/**
 * @typedef {object} ContextOptions
 * @property {any} [input]              ctx.input.
 * @property {{id: string, email: string, email_verified?: boolean, roles?: string[]} | null} [user] ctx.user.
 * @property {Record<string, string>} [secrets]  ctx.secrets.
 * @property {string | null} [idempotencyKey]
 * @property {string} [requestId]
 * @property {MemoryStore} [store]      Shared across several createContext calls, to seed data ctx.db reads.
 * @property {boolean} [admin]          Also give ctx.admin.db (function.yaml's admin: true).
 * @property {string[]} [calls]         function.yaml's `calls`: ctx.call refuses any other name with `call_not_declared`, as backd does.
 * @property {boolean | { perInvocation?: number, recipientsPerMessage?: number }} [email]  Give the function ctx.email.send (function.yaml's `email: true`); the object sets the realm's caps (`email.limits`) the fake enforces.
 */

/** The kinds backd sends itself: a function can't send them. */
const SYSTEM_EMAIL_KINDS = ['verify-email', 'reset-password', 'account-exists', 'password-changed', 'welcome', 'change-email', 'email-changed', 'invitation']

/**
 * An error `ctx.email.send` throws when one of the realm's caps is reached
 * (`email.limits`): `code` is `email_limited` and `retry_after` is in seconds.
 */
export class EmailLimitError extends Error {
  /** @param {string} message @param {number} [retryAfter] */
  constructor(message, retryAfter = 3600) {
    super(message)
    this.name = 'EmailLimitError'
    this.code = 'email_limited'
    this.retry_after = retryAfter
  }
}

/**
 * A fake of `ctx.email`: `send` records the email and refuses what backd
 * would: a missing or system `kind`, a message that doesn't name exactly one of
 * `to_user` and `to`, invalid addresses, more than `recipientsPerMessage`
 * recipients, and, past `perInvocation` messages from the same context, an
 * `EmailLimitError`. Any recipient is allowed: there is no list of who a
 * function may write to, so a function that takes the recipient or the
 * text from its input is a spam relay; test that yours doesn't (see the
 * docs, Functions -> Email). `sent()` lists what was sent.
 * `createContext({ email: true })` uses one.
 * @param {{ perInvocation?: number, recipientsPerMessage?: number }} [opts] The realm's `email.limits` (defaults 50 and 10).
 */
export function fakeEmail({ perInvocation = 50, recipientsPerMessage = 10 } = {}) {
  /** @type {Array<Record<string, any>>} */
  const sent = []
  let attempts = 0
  /** @param {string} message */
  const invalid = (message) => Object.assign(new TypeError(`ctx.email.send: ${message}`), { code: 'validation_error' })
  return {
    /** @param {Record<string, any>} [msg] */
    send: async (msg = {}) => {
      if (typeof msg.kind !== 'string' || msg.kind === '') throw invalid('kind is required')
      if (SYSTEM_EMAIL_KINDS.includes(msg.kind)) throw invalid(`${msg.kind} is a kind backd sends itself; a function sends the realm's own kinds`)
      const hasUser = msg.to_user !== undefined
      const hasTo = Array.isArray(msg.to) && msg.to.length > 0
      if (hasUser === hasTo) throw invalid('give exactly one of to_user (a user\'s id) and to (addresses)')
      if (hasUser && typeof msg.to_user !== 'string') throw invalid('to_user must be a user id')
      const addresses = [...(msg.to ?? []), ...(msg.cc ?? []), ...(msg.bcc ?? [])]
      if (!addresses.every((a) => typeof a === 'string' && /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(a))) throw invalid('to, cc and bcc are arrays of email addresses')
      if (addresses.length + (hasUser ? 1 : 0) > recipientsPerMessage) throw invalid(`at most ${recipientsPerMessage} recipients per message`)
      if (++attempts > perInvocation) throw new EmailLimitError(`too many emails (invocation limit of ${perInvocation})`)
      sent.push(structuredClone(msg))
      return { id: `email-job-${sent.length}`, status: 'queued' }
    },
    sent: () => structuredClone(sent),
  }
}

/**
 * A message as the realm's delivery function receives it as `ctx.input`
 * (see the docs, Functions -> Email), for testing a delivery function:
 * `createContext({ input: emailMessage({ kind: 'reset-password' }) })`.
 * @param {Record<string, any>} [overrides]
 */
export function emailMessage(overrides = {}) {
  return {
    id: 'email-test',
    kind: 'verify-email',
    from: 'Test <no-reply@example.com>',
    reply_to: null,
    to: [{ email: 'ana@example.com', name: null }],
    cc: [],
    bcc: [],
    subject: 'Confirm your email address',
    text: 'Open https://example.com/v1/test/_auth/verify-email?token=test-token',
    html: '<p><a href="https://example.com/v1/test/_auth/verify-email?token=test-token">Confirm</a></p>',
    locale: 'en',
    data: { link: 'https://example.com/v1/test/_auth/verify-email?token=test-token', expires_at: '2030-01-02T15:04:00Z' },
    ...overrides,
  }
}

/**
 * What a faked async callee returns, like the job handle `ctx.call` gives:
 * `await job.wait()` is the output, `job.status()` is `done`.
 * @param {{ id?: string, output?: unknown }} [opts]
 */
export function fakeJob({ id = 'job-test', output = null } = {}) {
  return { id, status: async () => 'done', wait: async () => output }
}

/**
 * The error a callee that ran out of time makes `ctx.call` throw: fake it
 * with `fakeCall(name, () => { throw callTimedOut() })`.
 */
export function callTimedOut() {
  return new BackdError({ status: 504, code: 'function_timeout', message: "the function didn't finish in time" })
}

/**
 * Builds a fake ctx for one call to a function's handler, backed by an
 * in-memory store (a fresh one per call unless `store` is given, so
 * several calls in one test can share state). Also returns `fakeCall` to
 * fake what `ctx.call(name, ...)` answers, `calls` to list what the
 * function called, `sentEmails` to list what it sent with `ctx.email.send`, and `steps`
 * to list what it reported with `ctx.step` and `ctx.progress` (name, total, message, the
 * last `current`, and every `updates` entry).
 * @param {ContextOptions} [opts]
 * @returns {{ ctx: any, store: MemoryStore, fakeCall: (name: string, handler: (input: any, options: any) => any) => void, calls: (name: string) => Array<{ input: any, options: any }>, sentEmails: () => Array<Record<string, any>>, steps: () => Array<{ name: string, total: number | null, message: string | null, current: number, updates: Array<{ current: number, message: string | null }> }> }}
 */
export function createContext(opts = {}) {
  const store = opts.store ?? new MemoryStore()
  const actor = opts.user ? `user:${opts.user.id}` : 'func:test'
  const ctx = {
    input: opts.input ?? null,
    user: opts.user ?? null,
    secrets: Object.freeze({ ...(opts.secrets ?? {}) }),
    idempotencyKey: opts.idempotencyKey ?? null,
    requestId: opts.requestId ?? 'test-request',
    db: (name) => new FakeDatabase(store, name, actor),
    error: (status, code, message, details) => {
      if (!Number.isInteger(status) || status < 400 || status > 499) {
        throw new TypeError('ctx.error: status must be a 4xx code')
      }
      return new FunctionError(status, String(code), String(message ?? code), details)
    },
  }
  if (opts.admin) {
    ctx.admin = Object.freeze({ db: (name) => new FakeDatabase(store, name, 'func:test') })
  }

  // ctx.call: only what the test fakes, and only what `calls` declares.
  /** @type {Map<string, (input: any, options: any) => any>} */
  const fakes = new Map()
  /** @type {Array<{ name: string, input: any, options: any }>} */
  const recorded = []
  ctx.call = async (name, input = null, options = {}) => {
    if (opts.calls && !opts.calls.includes(name)) {
      throw new FunctionError(403, 'call_not_declared', `this function didn't declare ${name} in its \`calls\``)
    }
    const handler = fakes.get(name)
    if (!handler) {
      throw new Error(`@backd/functions-testing: ctx.call(${JSON.stringify(name)}) isn't faked: use fakeCall(${JSON.stringify(name)}, handler)`)
    }
    recorded.push({ name, input, options })
    return await handler(input, options)
  }

  // ctx.step / ctx.progress: recorded, with what the real ones would refuse.
  /** @type {Array<{ name: string, total: number | null, message: string | null, current: number, updates: Array<{ current: number, message: string | null }> }>} */
  const reported = []
  const count = (what, v) => {
    if (typeof v !== 'number' || !Number.isFinite(v) || v < 0) throw new TypeError(`${what} must be a number that is not negative`)
    return v
  }
  ctx.step = (name, options = {}) => {
    if (typeof name !== 'string' || name === '') throw new TypeError('ctx.step: the name must be a non-empty string')
    if (options === null || typeof options !== 'object') throw new TypeError('ctx.step: options must be an object')
    reported.push({
      name,
      total: options.total === undefined ? null : count('ctx.step: total', options.total),
      message: options.message === undefined ? null : String(options.message),
      current: 0,
      updates: [],
    })
  }
  ctx.progress = (current, message) => {
    count('ctx.progress: current', current)
    if (reported.length === 0) ctx.step('progress')
    const step = reported[reported.length - 1]
    step.current = current
    if (message !== undefined) step.message = message === null ? null : String(message)
    step.updates.push({ current, message: message === undefined ? null : message === null ? null : String(message) })
  }

  // ctx.email.send: records the email; checks what backd would refuse.
  const mail = fakeEmail(typeof opts.email === 'object' ? opts.email : {})
  if (opts.email) ctx.email = Object.freeze({ send: mail.send })
  return {
    ctx: Object.freeze(ctx),
    store,
    sentEmails: mail.sent,
    steps: () => reported.map((st) => ({ ...st, updates: st.updates.map((u) => ({ ...u })) })),
    fakeCall: (name, handler) => void fakes.set(name, handler),
    calls: (name) => recorded.filter((c) => c.name === name).map(({ input, options }) => ({ input, options })),
  }
}
