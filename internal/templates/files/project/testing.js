// A fake ctx for testing this project's functions with `deno test`,
// without a running backd, an executor or a network — vendored from
// @backd/functions-testing (backd's clients/functions-testing, not yet
// published; this copy inlines its two error classes so lib/ has no
// dependency outside this file). See stats/index.test.ts for an example,
// and the backd docs, Functions → Build, dev and test → "Unit testing a
// function's logic", for the full API and what it deliberately leaves out
// (no access rules, a practical subset of the query language).

/** Thrown by ctx.db(...).get/replace/patch/delete when the document doesn't exist. */
export class NotFoundError extends Error {
  constructor(message) {
    super(message)
    this.name = 'NotFoundError'
    this.status = 404
    this.code = 'not_found'
  }
}

/** Thrown by replace/patch/delete when ifMatch doesn't match the current version. */
export class VersionMismatchError extends Error {
  constructor(message) {
    super(message)
    this.name = 'VersionMismatchError'
    this.status = 412
    this.code = 'version_mismatch'
  }
}

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

function ifMatchWant(ifMatch) {
  return typeof ifMatch === 'number' ? String(ifMatch) : String(ifMatch).replace(/^"|"$/g, '')
}

function checkIfMatch(doc, ifMatch) {
  if (ifMatch === undefined) return
  const want = ifMatchWant(ifMatch)
  if (want === '*') return
  if (String(doc._meta.version) !== want) {
    throw new VersionMismatchError(`If-Match ${ifMatch} doesn't match the current version`)
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
    if (!fn) throw new TypeError(`testing.js: unsupported where operator ${op}`)
    return fn(value, want)
  })
}

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
    this.byCollection = new Map()
  }

  collection(database, name) {
    const key = `${database}/${name}`
    let c = this.byCollection.get(key)
    if (!c) {
      c = new Map()
      this.byCollection.set(key, c)
    }
    return c
  }

  /** Seeds a collection with documents; missing id/_meta are filled in. */
  seed(database, name, docs) {
    const c = this.collection(database, name)
    for (const doc of docs) {
      const id = doc.id ?? nextId()
      c.set(id, { ...doc, id, _meta: { version: 1, created_at: now(), updated_at: now(), owner: null, ...doc._meta } })
    }
    return this
  }

  /** All documents currently in a collection, for assertions. */
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
   * @returns {Promise<{ items: any[], limit: number, skip: number, has_more: boolean, total?: number }>}
   */
  async list({ where, orderBy, limit = 20, skip = 0, count } = {}) {
    let items = [...this.map.values()].filter(matchWhere(where))
    if (orderBy) items = sortBy(items, orderBy)
    const page = items.slice(skip, skip + limit)
    /** @type {{ items: any[], limit: number, skip: number, has_more: boolean, total?: number }} */
    const out = { items: page, limit, skip, has_more: skip + limit < items.length }
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
    if (!doc) throw new NotFoundError(`document not found: ${id}`)
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

/** A function's error, as ctx.error(status, code, message, details) returns it. */
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
 * Builds a fake ctx for one call to a function's handler, backed by an
 * in-memory store (a fresh one per call unless `store` is given, so
 * several calls in one test can share state).
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
  return { ctx: Object.freeze(ctx), store }
}
