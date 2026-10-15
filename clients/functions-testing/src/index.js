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
// are not evaluated — ctx.db here is full access, as if admin: true —
// unless a test declares a read predicate (store.declareReadRule) to see
// what a function does when a document isn't readable; references are
// declared with store.declareReferences and followed by `expand`. The point
// is testing the function's own code, not backd's rule engine (that's what
// the real integration tests are for).
import { BackdError, NotFoundError, VersionMismatchError } from '../../js/src/errors.js'
import { DEFAULT_LIMITS, expandDocs, normalizeReferences, planExpand } from './expand.js'

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

/**
 * What a file field declares (`files:` in collection.yaml).
 * @typedef {object} FileFieldConfig
 * @property {boolean} [multiple]
 * @property {number} [max_files]
 * @property {number} [max_size]
 * @property {string[]} [types]   Content types, or families such as `image/*`.
 * @property {Record<string, VersionConfig>} [versions]   Declared image versions (`versions:`).
 */

/**
 * A declared version, as in `collection.yaml`: a box (`max_width`, `max_height`), how it fits
 * (`fit`), `quality`, `format`, `upscale`, and `writable`.
 * @typedef {object} VersionConfig
 * @property {number} [max_width]
 * @property {number} [max_height]
 * @property {'contain' | 'cover' | 'stretch'} [fit]
 * @property {number} [quality]
 * @property {'jpeg' | 'png'} [format]
 * @property {boolean} [upscale]
 * @property {boolean} [writable]
 */

/** An in-memory store shared by every ctx.db(...) call in one test. */
export class MemoryStore {
  constructor() {
    /** @type {Map<string, Map<string, any>>} */
    this.byCollection = new Map()
    /** @type {Map<string, Uint8Array>} */
    this.objects = new Map()
    /** @type {Map<string, Record<string, FileFieldConfig>>} */
    this.fileFields = new Map()
    /** @type {Map<string, Record<string, any>>} */
    this.references = new Map()
    /** @type {Map<string, (doc: any, who: { user: any }) => boolean>} */
    this.readRules = new Map()
  }

  /**
   * Declares a collection's references, as `references:` in its collection.yaml does, so that
   * `expand` works: `{ author: { field: '_meta.owner', collection: 'profiles', key: '_meta.owner' },
   * tags: { collection: 'tags', many: true } }`. `database` defaults to this one, `field` to the
   * reference's name and `key` to `id`; `max_expand` lowers the array cap.
   * @param {string} database
   * @param {string} name
   * @param {Record<string, { collection: string, field?: string, database?: string, key?: string, many?: boolean, max_expand?: number }>} refs
   */
  declareReferences(database, name, refs) {
    this.references.set(`${database}/${name}`, normalizeReferences(database, refs))
    return this
  }

  /**
   * Says which documents a caller may read in a collection, as its `read` rule does: a
   * predicate `(doc, { user }) => boolean`. It applies to `ctx.db` (not to `ctx.admin.db`): to
   * `get`, `list` and what `expand` returns, where an unreadable reference is `null`, as on
   * backd. Without one every document is readable, like the rest of the fake.
   * @param {string} database
   * @param {string} name
   * @param {(doc: any, who: { user: any }) => boolean} rule
   */
  declareReadRule(database, name, rule) {
    this.readRules.set(`${database}/${name}`, rule)
    return this
  }

  /** @private The view `expand` needs of the store. */
  get expandSource() {
    return {
      collection: (database, name) => this.collection(database, name),
      referencesOf: (database, name) => this.references.get(`${database}/${name}`),
      readRuleOf: (database, name) => this.readRules.get(`${database}/${name}`),
    }
  }

  /**
   * Declares a collection's file fields, as `files:` in its collection.yaml does, so the
   * fake enforces what backd does for them: `multiple` (a list; `put` adds), `max_files`,
   * `max_size` (bytes) and `types`. A field that isn't declared holds one file, unlimited.
   * @param {string} database
   * @param {string} name
   * @param {Record<string, FileFieldConfig>} fields
   */
  declareFiles(database, name, fields) {
    this.fileFields.set(`${database}/${name}`, fields)
    return this
  }

  /**
   * The bytes stored for a file id, for assertions after calling the function under test.
   * @param {string} fileId
   * @returns {Uint8Array | undefined}
   */
  fileBytes(fileId) {
    return this.objects.get(fileId)
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
  /**
   * @param {MemoryStore} store
   * @param {string} database
   * @param {string} name
   * @param {string | null} [actor]
   * @param {{ user: any, admin: boolean, expand: typeof DEFAULT_LIMITS }} [who] The identity reads are made as.
   */
  constructor(store, database, name, actor, who = { user: null, admin: true, expand: DEFAULT_LIMITS }) {
    this.store = store
    this.database = database
    this.name = name
    this.map = store.collection(database, name)
    this.actor = actor ?? null
    this.who = who
  }

  /** @private The documents the caller may read. */
  readable() {
    const rule = this.who.admin ? undefined : this.store.readRules.get(`${this.database}/${this.name}`)
    const all = [...this.map.values()]
    return rule ? all.filter((d) => rule(d, { user: this.who.user })) : all
  }

  /** @private Expands what `expand` asks for in copies of docs. */
  expanded(docs, expand) {
    if (expand === undefined) return docs
    const plan = planExpand(this.store.expandSource, this.database, this.name, expand, this.who.expand)
    return plan.length === 0 ? docs : expandDocs(this.store.expandSource, docs, plan, this.who, this.who.expand)
  }

  /**
   * The files of a document's file field: `get`, `put`, `delete`, `link`, as the real client
   * has them, over the store's memory. No access rules, like the rest of the fake.
   * @param {string} id
   * @param {string} field
   */
  files(id, field) {
    return new FakeFiles(this, id, field)
  }

  /**
   * The fake's `after` is the offset of the next page (its `next_cursor`):
   * enough to exercise a function that pages with cursors, not the real
   * cursor's position semantics.
   * @returns {Promise<{ items: any[], limit: number, skip: number, has_more: boolean, next_cursor?: string, total?: number }>}
   */
  async list({ where, orderBy, limit = 20, skip = 0, after, count, expand } = {}) {
    if (after !== undefined) {
      if (skip) throw new Error('after can\'t be combined with skip')
      skip = Number(after)
      if (!Number.isInteger(skip) || skip < 0) throw new Error('after is not a cursor of this fake: use the next_cursor of a previous page')
    }
    let items = this.readable().filter(matchWhere(where))
    if (orderBy) items = sortBy(items, orderBy)
    const page = this.expanded(items.slice(skip, skip + limit), expand)
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

  async get(id, opts = {}) {
    const rule = this.who.admin ? undefined : this.store.readRules.get(`${this.database}/${this.name}`)
    const doc = this.map.get(id)
    if (!doc || (rule && !rule(doc, { user: this.who.user }))) {
      throw new NotFoundError({ status: 404, code: 'not_found', message: `document not found: ${id}` })
    }
    return this.expanded([doc], opts.expand)[0]
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

/** @param {string | Uint8Array | ArrayBuffer | Blob | ReadableStream} data */
async function toBytes(data) {
  if (typeof data === 'string') return new TextEncoder().encode(data)
  if (data instanceof Uint8Array) return data
  if (data instanceof ArrayBuffer) return new Uint8Array(data)
  if (typeof Blob !== 'undefined' && data instanceof Blob) return new Uint8Array(await data.arrayBuffer())
  return new Uint8Array(await new Response(/** @type {ReadableStream} */ (data)).arrayBuffer())
}

let fileSeq = 0

/** The fake of `collection.files(id, field)`. */
class FakeFiles {
  constructor(collection, id, field) {
    this.collection = collection
    this.id = id
    this.field = field
  }

  /** @private */
  get config() {
    const { database, name, store } = this.collection
    return store.fileFields.get(`${database}/${name}`)?.[this.field] ?? {}
  }

  /** @private */
  held(doc) {
    const v = doc[this.field]
    return v == null ? [] : Array.isArray(v) ? v : [v]
  }

  async list() {
    return this.held(await this.collection.get(this.id))
  }

  /** @private */
  async which(fileId) {
    if (fileId !== undefined) return fileId
    const files = await this.list()
    if (files.length === 0) throw new NotFoundError({ status: 404, code: 'not_found', message: `the document holds no file in ${this.field}` })
    if (files.length > 1) throw new TypeError(`files(${this.field}): the field holds several files: say which one (a fileId)`)
    return files[0].id
  }

  async get(fileId) {
    const id = await this.which(fileId)
    const file = (await this.list()).find((f) => f.id === id)
    const bytes = this.collection.store.objects.get(id)
    if (!file || !bytes) throw new NotFoundError({ status: 404, code: 'not_found', message: 'file not found' })
    return {
      file,
      response: new Response(bytes, { headers: { 'Content-Type': file.type } }),
      bytes: async () => bytes,
      text: async () => new TextDecoder().decode(bytes),
    }
  }

  async put(data, opts = {}) {
    const bytes = await toBytes(data)
    const type = opts.type ?? (typeof Blob !== 'undefined' && data instanceof Blob && data.type ? data.type : 'application/octet-stream')
    const cfg = this.config
    const refuse = (status, code, message) => new BackdError({ status, code, message })
    if (cfg.max_size !== undefined && bytes.length > cfg.max_size) throw refuse(413, 'payload_too_large', `the file is over ${cfg.max_size} bytes`)
    if (cfg.types?.length && !cfg.types.some((t) => t === type || (t.endsWith('/*') && type.startsWith(t.slice(0, -1))))) {
      throw refuse(415, 'unsupported_file_type', `this field doesn't accept ${type}`)
    }
    const doc = await this.collection.get(this.id)
    checkIfMatch(doc, opts.ifMatch)
    const existing = this.held(doc)
    if (cfg.multiple && existing.length >= (cfg.max_files ?? 10)) throw refuse(409, 'too_many_files', `this field already holds ${existing.length} files (max_files)`)
    const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
    const file = {
      id: `fl_test${String(++fileSeq).padStart(16, '0')}`,
      name: sanitizeName(opts.name),
      size: bytes.length,
      type,
      sha256: [...digest].map((b) => b.toString(16).padStart(2, '0')).join(''),
      uploaded_at: now(),
    }
    const size = pngSize(bytes)
    if (size) Object.assign(file, size)
    if (cfg.versions) file.versions = initialVersions(cfg.versions, type, size)
    this.collection.store.objects.set(file.id, bytes)
    for (const old of cfg.multiple ? [] : existing) this.collection.store.objects.delete(old.id)
    const next = { ...doc, [this.field]: cfg.multiple ? [...existing, file] : file }
    next._meta = { ...doc._meta, version: doc._meta.version + 1, updated_at: now() }
    this.collection.map.set(this.id, next)
    return next
  }

  async delete(fileId, opts = {}) {
    const doc = await this.collection.get(this.id)
    checkIfMatch(doc, opts.ifMatch)
    const existing = this.held(doc)
    const gone = fileId === undefined ? existing : existing.filter((f) => f.id === fileId)
    if (fileId !== undefined && gone.length === 0) throw new NotFoundError({ status: 404, code: 'not_found', message: 'file not found' })
    for (const f of gone) this.collection.store.objects.delete(f.id)
    const keep = existing.filter((f) => !gone.includes(f))
    const next = { ...doc }
    if (keep.length === 0) delete next[this.field]
    else next[this.field] = this.config.multiple ? keep : keep[0]
    next._meta = { ...doc._meta, version: doc._meta.version + 1, updated_at: now() }
    this.collection.map.set(this.id, next)
    return next
  }

  /**
   * One of the file's declared versions: `regenerate()`, `generate(params)` and `delete()`,
   * with the errors backd gives (`version_not_writable`, `not_an_image`, validation). The fake
   * makes no pictures: a made version is `ready` with the size the box gives (the dimensions
   * of a PNG are read; other pictures have none), so a function's logic can be tested.
   * @param {string} name
   * @param {string} [fileId]
   */
  version(name, fileId) {
    const fail = (status, code, message) => new BackdError({ status, code, message })
    const target = async () => {
      const id = await this.which(fileId)
      const doc = await this.collection.get(this.id)
      const file = this.held(doc).find((f) => f.id === id)
      if (!file) throw new NotFoundError({ status: 404, code: 'not_found', message: 'file not found' })
      const declared = this.config.versions?.[name]
      if (!declared) throw new NotFoundError({ status: 404, code: 'not_found', message: `this field declares no version ${name}` })
      return { doc, file, declared }
    }
    const record = (doc, file, state) => {
      const files = this.held(doc).map((f) => (f.id === file.id ? { ...f, versions: { ...f.versions, [name]: state } } : f))
      // Like backd, the document's version does not change.
      this.collection.map.set(this.id, { ...doc, [this.field]: this.config.multiple ? files : files[0], _meta: { ...doc._meta, updated_at: now() } })
      return state
    }
    const make = async (params, custom) => {
      const { doc, file } = await target()
      if (!file.type.startsWith('image/')) throw fail(422, 'not_an_image', 'the version could not be made: not_an_image')
      const dims = file.width ? fitBox(file.width, file.height, params) : {}
      const state = {
        status: 'ready', id: `fv_test${String(++fileSeq).padStart(15, '0')}`, size: 1, type: params.format === 'png' || (!params.format && file.type === 'image/png') ? 'image/png' : 'image/jpeg',
        ...dims, params: { ...params }, fingerprint: JSON.stringify(params),
      }
      if (custom) state.custom = true
      return record(doc, file, state)
    }
    return {
      regenerate: async () => {
        const { declared } = await target()
        if (!declared.max_width && !declared.max_height) throw fail(400, 'validation_failed', `the version ${name} has no parameters of its own: generate it with max_width or max_height`)
        const { writable, ...params } = declared
        return make(params, false)
      },
      generate: async (params) => {
        const { declared } = await target()
        if (!declared.writable) throw fail(403, 'version_not_writable', `the version ${name} is not writable: declare writable: true to generate it with other parameters`)
        const problems = versionParamProblems(params ?? {})
        if (problems.length) throw fail(400, 'validation_failed', `the parameters are not valid: ${problems.join('; ')}`)
        return make(params, true)
      },
      delete: async () => {
        const { doc, file, declared } = await target()
        return record(doc, file, declared.max_width || declared.max_height ? { status: 'pending' } : { status: 'empty' })
      },
    }
  }

  async link(fileId) {
    const id = await this.which(fileId)
    if (!(await this.list()).some((f) => f.id === id)) throw new NotFoundError({ status: 404, code: 'not_found', message: 'file not found' })
    return { url: `https://files.test/${this.field}/${id}`, expires_at: new Date(Date.now() + 5 * 60_000).toISOString() }
  }
}

/** The pixel size of a PNG, from its header; null for anything else. */
function pngSize(bytes) {
  const sig = [137, 80, 78, 71, 13, 10, 26, 10]
  if (bytes.length < 24 || sig.some((b, i) => bytes[i] !== b)) return null
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  return { width: view.getUint32(16), height: view.getUint32(20) }
}

/** The state of each declared version of a new file, as backd records it on upload. */
function initialVersions(declared, type, size) {
  const out = {}
  for (const [name, v] of Object.entries(declared)) {
    if (!type.startsWith('image/')) out[name] = { status: 'skipped', reason: 'not_an_image' }
    else out[name] = v.max_width || v.max_height ? { status: 'pending' } : { status: 'empty' }
  }
  void size
  return out
}

/** The size a picture of w × h gets in a box, as backd's image engine does it. */
function fitBox(w, h, p) {
  let mw = p.max_width ?? 0
  let mh = p.max_height ?? 0
  if (!p.upscale) {
    if (mw > w) mw = w
    if (mh > h) mh = h
  }
  const fit = p.fit ?? 'contain'
  if (fit === 'stretch') return { width: mw || w, height: mh || h }
  if (fit === 'cover' && mw && mh) return { width: mw, height: mh }
  let s = Infinity
  if (mw) s = Math.min(s, mw / w)
  if (mh) s = Math.min(s, mh / h)
  if (!p.upscale && s > 1) s = 1
  return { width: Math.max(1, Math.round(w * s)), height: Math.max(1, Math.round(h * s)) }
}

/** What is wrong with parameters given at run time, with backd's rules. */
function versionParamProblems(p) {
  const known = new Set(['max_width', 'max_height', 'fit', 'quality', 'format', 'upscale'])
  const out = []
  for (const k of Object.keys(p)) if (!known.has(k)) out.push(`unknown parameter ${k}`)
  for (const k of ['max_width', 'max_height']) {
    if (p[k] !== undefined && !(Number.isInteger(p[k]) && p[k] >= 1 && p[k] <= 16384)) out.push(`${k}: must be between 1 and 16384`)
  }
  if (!p.max_width && !p.max_height) out.push('at least one of max_width and max_height is required')
  if (p.fit !== undefined && !['contain', 'cover', 'stretch'].includes(p.fit)) out.push('fit: must be contain, cover or stretch')
  if (p.fit === 'cover' && !(p.max_width && p.max_height)) out.push('fit: cover needs both max_width and max_height')
  if (p.format !== undefined && !['jpeg', 'png'].includes(p.format)) out.push('format: must be jpeg or png')
  if (p.quality !== undefined && !(Number.isInteger(p.quality) && p.quality >= 1 && p.quality <= 100)) out.push('quality: must be between 1 and 100')
  if (p.quality !== undefined && p.format === 'png') out.push('quality: only applies to format jpeg')
  return out
}

/** What backd does to a file's name, roughly: no path, no control characters, `file` when nothing is left. */
function sanitizeName(name) {
  const base = String(name ?? '').normalize('NFC').split(/[\\/]/).pop() ?? ''
  const clean = base.replace(/[\u0000-\u001f\u007f]/g, '').trim()
  return clean === '' ? 'file' : clean.slice(0, 255)
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
  constructor(store, name, actor, who) {
    this.store = store
    this.name = name
    this.actor = actor
    this.who = who
  }

  collection(name) {
    return new FakeCollection(this.store, this.name, name, this.actor, this.who)
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
 * @property {{ maxDepth?: number, maxPaths?: number, maxArray?: number, maxDocs?: number }} [expand] Lower the expansion limits (backd's defaults: 2 levels, 5 paths, 100 ids, 2000 documents) to test a function against them.
 * @property {{id: string, email: string, email_verified?: boolean, roles?: string[], consents?: Record<string, string>} | null} [user] ctx.user.
 * @property {Record<string, string>} [secrets]  ctx.secrets.
 * @property {string | null} [idempotencyKey]
 * @property {string} [requestId]
 * @property {MemoryStore} [store]      Shared across several createContext calls, to seed data ctx.db reads.
 * @property {boolean} [admin]          Also give ctx.admin.db (function.yaml's admin: true).
 * @property {string[]} [calls]         function.yaml's `calls`: ctx.call refuses any other name with `call_not_declared`, as backd does.
 * @property {boolean | { kinds?: string[], audience?: boolean }} [push]  Give the function ctx.push.send (function.yaml's `push:`); the object is what it declares: the `kinds` it may name and whether it may send to an `audience`.
 * @property {boolean | { perInvocation?: number, recipientsPerMessage?: number, consents?: Record<string, string[]> }} [email]  Give the function ctx.email.send (function.yaml's `email: true`); the object sets the realm's caps (`email.limits`) the fake enforces, and the consents each user id holds, for sends that `requires_consent`.
 * @property {Record<string, any>} [config]  The realm's app configuration, nested like its schema.json: gives the function ctx.config.get and ctx.config.all.
 * @property {boolean | { orgs?: Array<{ id?: string, name: string, slug?: string | null, claims?: Record<string, any>, members?: Array<{ user: string, roles?: string[], teams?: string[], teamRoles?: Array<{ team: string, role: string }> }>, teams?: Array<{ id?: string, name: string, roles?: string[] }> }> }} [orgs]  Also give ctx.admin.orgs: create, get, list, setClaims, addMember, removeMember, teams and invitations, kept in memory (start from `orgs`); \`orgChanges()\` lists what the function changed. \`user.orgs\` is passed through as given.
* @property {boolean} [configWrite]    Also give ctx.admin.config.set, reset and history (function.yaml's `config: write`); changes apply to `config` and are listed by `configChanges()`.
 */

/** The kinds backd sends itself: a function can't send them. */
const SYSTEM_EMAIL_KINDS = [
  'verify-email', 'reset-password', 'account-exists', 'password-changed', 'welcome', 'change-email', 'email-changed', 'invitation',
  'security-notice', 'mfa-failed', 'reauth-code', 'mfa-reset', 'magic-link', 'no-account', 'consent-updated',
]

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
 * With `requires_consent` (a `to_user` only) the fake answers `{ status: 'no_consent' }` and
 * records nothing, unless `consents` says the user holds it.
 * `createContext({ email: true })` uses one.
 * @param {{ perInvocation?: number, recipientsPerMessage?: number, consents?: Record<string, string[]> }} [opts]
 *   The realm's `email.limits` (defaults 50 and 10), and the consents each user id holds (none by default).
 */
export function fakeEmail({ perInvocation = 50, recipientsPerMessage = 10, consents = {} } = {}) {
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
      if (msg.org !== undefined && (typeof msg.org !== 'string' || msg.org === '')) throw invalid('org must be an organization id')
      if (msg.requires_consent !== undefined) {
        if (typeof msg.requires_consent !== 'string' || !hasUser) throw invalid('requires_consent needs to_user: consents belong to users, not to addresses')
        if (!(consents[msg.to_user] ?? []).includes(msg.requires_consent)) return { status: 'no_consent' }
      }
      if (++attempts > perInvocation) throw new EmailLimitError(`too many emails (invocation limit of ${perInvocation})`)
      sent.push(structuredClone(msg))
      return { id: `email-job-${sent.length}`, status: 'queued' }
    },
    sent: () => structuredClone(sent),
  }
}

/**
 * A fake of `ctx.push`: `send` records the push and refuses what backd would before queueing
 * it: nobody (or more than one way) named, an audience from a function that didn't declare
 * `audience: true`, a kind outside the declared `kinds`, a message with a field backd would not
 * accept, and one over a provider's size. Like backd it takes `user`, `users` or `audience`,
 * and `kind` or `title` and `body`. Failures are a `FunctionError` with backd's codes
 * (`invalid_push_message`, `unknown_push_kind`, `payload_too_large`, `push_audience_not_declared`).
 * It sends nothing and counts no limits: `sent()` lists what the function asked for.
 * With `requires_consent`, users who don't hold the consent (`consents` maps a user id to the
 * consents it holds) are counted in `no_consent` and not in `queued`.
 * `createContext({ push: true })` uses one.
 * @param {{ kinds?: string[], audience?: boolean, consents?: Record<string, string[]> }} [opts]
 *   What `push:` declares in function.yaml, and the consents each user id holds (none by default).
 */
export function fakePush({ kinds, audience = false, consents = {} } = {}) {
  /** @type {Array<Record<string, any>>} */
  const sent = []
  /** @param {string} reason @param {string} [field] */
  const invalid = (reason, field = 'message') => new FunctionError(400, 'invalid_push_message', `${field}: ${reason}`, [{ path: field, reason }])
  return {
    /** @param {Record<string, any>} [msg] */
    send: async (msg = {}) => {
      const who = ['user', 'users', 'audience'].filter((k) => msg[k] !== undefined && !(Array.isArray(msg[k]) && msg[k].length === 0))
      if (who.length !== 1) throw invalid('give exactly one of user, users and audience', 'user')
      if (msg.audience !== undefined && !audience) {
        throw new FunctionError(403, 'push_audience_not_declared', "this function didn't declare `push: { audience: true }` in its function.yaml")
      }
      if (msg.audience !== undefined && msg.audience !== 'all' && (msg.audience === null || typeof msg.audience !== 'object')) {
        throw invalid('must be "all" or an object', 'audience')
      }
      if (msg.user !== undefined && typeof msg.user !== 'string') throw invalid('must be a user id', 'user')
      if (msg.users !== undefined && !(Array.isArray(msg.users) && msg.users.every((u) => typeof u === 'string'))) throw invalid('must be an array of user ids', 'users')
      const raw = msg.kind === undefined || msg.kind === 'raw'
      if (!raw) {
        if (kinds !== undefined && !kinds.includes(msg.kind)) throw new FunctionError(400, 'unknown_push_kind', `this function didn't declare the push kind ${msg.kind}`)
        if (msg.title !== undefined || msg.body !== undefined) throw invalid('a message with a kind takes its text from the template: give kind, or title and body', 'title')
      }
      if (msg.silent === true) {
        if (!msg.data || Object.keys(msg.data).length === 0) throw invalid('a silent push carries only data: it is required', 'data')
        for (const k of ['title', 'body', 'badge', 'sound']) if (msg[k] !== undefined) throw invalid(`a silent push takes no ${k}`, k)
        if (!raw) throw invalid('a silent push has no template: it takes no kind', 'kind')
      } else if (raw && (typeof msg.title !== 'string' || msg.title.trim() === '' || typeof msg.body !== 'string' || msg.body.trim() === '')) {
        throw invalid('title and body are required', 'title')
      }
      if (msg.data !== undefined) {
        if (msg.data === null || typeof msg.data !== 'object' || Array.isArray(msg.data)) throw invalid('must be an object of strings', 'data')
        for (const [k, v] of Object.entries(msg.data)) if (typeof v !== 'string') throw invalid(`${k}: data values are strings`, 'data')
        if (JSON.stringify(msg.data).length > 2048) throw invalid('at most 2048 bytes serialized', 'data')
      }
      if (msg.group !== undefined && !/^[A-Za-z0-9_-]{1,32}$/.test(msg.group)) throw invalid('1 to 32 letters, digits, _ or -', 'group')
      if (msg.priority !== undefined && !['normal', 'high'].includes(msg.priority)) throw invalid('must be normal or high', 'priority')
      if (msg.badge !== undefined && !(Number.isInteger(msg.badge) && msg.badge >= 0)) throw invalid('must be 0 or more', 'badge')
      // The smallest provider limit (Web Push's 3,993 bytes), on the portable fields.
      if (JSON.stringify({ title: msg.title, body: msg.body, data: msg.data, link: msg.link, platform: msg.platform }).length > 3993) {
        throw new FunctionError(413, 'payload_too_large', 'the message is over what a provider takes')
      }
      if (msg.requires_consent !== undefined && typeof msg.requires_consent !== 'string') throw invalid('must be a consent name', 'requires_consent')
      sent.push(structuredClone(msg))
      const named = msg.user !== undefined ? [msg.user] : Array.isArray(msg.users) ? [...new Set(msg.users)] : null
      const users = named === null ? null : named.length
      const noConsent = named === null || msg.requires_consent === undefined ? 0 : named.filter((u) => !(consents[u] ?? []).includes(msg.requires_consent)).length
      const queued = users === null ? null : users - noConsent
      return { send_id: `ps_test${sent.length}`, users, jobs: 1, queued, limited: users === null ? null : 0, no_devices: users === null ? null : 0, no_consent: users === null ? null : noConsent }
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
 * function called, `sentEmails` to list what it sent with `ctx.email.send`, `sentPushes` to list what it sent with `ctx.push.send`, `configChanges` to list what it changed with `ctx.admin.config`, and `steps`
 * to list what it reported with `ctx.step` and `ctx.progress` (name, total, message, the
 * last `current`, and every `updates` entry).
 * @param {ContextOptions} [opts]
 * @returns {{ ctx: any, store: MemoryStore, fakeCall: (name: string, handler: (input: any, options: any) => any) => void, calls: (name: string) => Array<{ input: any, options: any }>, sentEmails: () => Array<Record<string, any>>, sentPushes: () => Array<Record<string, any>>, orgChanges: () => Array<{ action: string, org: string, [key: string]: any }>, configChanges: () => Array<{ path: string, old: any, new: any }>, steps: () => Array<{ name: string, total: number | null, message: string | null, current: number, updates: Array<{ current: number, message: string | null }> }> }}
 */
export function createContext(opts = {}) {
  const store = opts.store ?? new MemoryStore()
  const actor = opts.user ? `user:${opts.user.id}` : 'func:test'
  const limits = { ...DEFAULT_LIMITS, ...(opts.expand ?? {}) }
  const ctx = {
    input: opts.input ?? null,
    user: opts.user ?? null,
    secrets: Object.freeze({ ...(opts.secrets ?? {}) }),
    idempotencyKey: opts.idempotencyKey ?? null,
    requestId: opts.requestId ?? 'test-request',
    db: (name) => new FakeDatabase(store, name, actor, { user: opts.user ?? null, admin: false, expand: limits }),
    error: (status, code, message, details) => {
      if (!Number.isInteger(status) || status < 400 || status > 499) {
        throw new TypeError('ctx.error: status must be a 4xx code')
      }
      return new FunctionError(status, String(code), String(message ?? code), details)
    },
  }
  if (opts.admin) {
    ctx.admin = Object.freeze({ db: (name) => new FakeDatabase(store, name, 'func:test', { user: null, admin: true, expand: limits }) })
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
  // ctx.push.send: records the push; checks what backd would refuse before queueing it.
  const pushes = fakePush(typeof opts.push === 'object' ? opts.push : {})
  if (opts.push) ctx.push = Object.freeze({ send: pushes.send })

  // ctx.config (and ctx.admin.config with `configWrite`): the values the test gives, nested.
  const config = structuredClone(opts.config ?? {})
  const at = (path) => String(path).split('.').reduce((v, k) => (v !== null && typeof v === 'object' ? v[k] : undefined), config)
  /** @type {Array<{ path: string, old: any, new: any }>} */
  const configChanged = []
  /** @type {Array<{ version: string, actor: string, kind: string, changes: Array<{ path: string, old: any, new: any }> }>} */
  const configHistory = []
  const put = (path, value) => {
    const keys = String(path).split('.')
    let cur = config
    for (const k of keys.slice(0, -1)) cur = cur[k] ??= {}
    cur[keys[keys.length - 1]] = value
  }
  if (opts.config) {
    ctx.config = Object.freeze({ all: async () => structuredClone(config), get: async (path) => structuredClone(at(path)) })
  }
  if (opts.configWrite) {
    const apply = (changes) => {
      const real = changes.filter((c) => JSON.stringify(c.old) !== JSON.stringify(c.new))
      for (const c of real) put(c.path, c.new)
      configChanged.push(...real)
      if (real.length > 0) configHistory.unshift({ version: `c${configHistory.length + 1}`, actor: 'function:test', kind: 'set', changes: real })
      return { version: `c${configHistory.length}`, changed: real.length > 0, changes: real }
    }
    ctx.admin = Object.freeze({
      ...(ctx.admin ?? {}),
      config: Object.freeze({
        set: async (values) => apply(Object.entries(values).map(([path, value]) => ({ path, old: structuredClone(at(path)), new: value }))),
        reset: async () => {
          throw new Error("@backd/functions-testing: ctx.admin.config.reset needs the schema's defaults, which the fake doesn't have: use set() to put the default back")
        },
        history: async (limit) => structuredClone(configHistory.slice(0, limit ?? 20)),
      }),
    })
  }
  // ctx.admin.orgs: organizations in memory. The real one is an admin's: no granting rules, but
  // the structure is checked as backd does (names and slugs unique, the last manager stays).
  const orgState = fakeOrgs(typeof opts.orgs === 'object' ? opts.orgs : {})
  if (opts.orgs) ctx.admin = Object.freeze({ ...(ctx.admin ?? {}), orgs: orgState.api })
  return {
    ctx: Object.freeze(ctx),
    store,
    orgChanges: orgState.changes,
    sentEmails: mail.sent,
    sentPushes: pushes.sent,
    configChanges: () => structuredClone(configChanged),
    steps: () => reported.map((st) => ({ ...st, updates: st.updates.map((u) => ({ ...u })) })),
    fakeCall: (name, handler) => void fakes.set(name, handler),
    calls: (name) => recorded.filter((c) => c.name === name).map(({ input, options }) => ({ input, options })),
  }
}

/**
 * A fake of `ctx.admin.orgs`: organizations, members, teams and invitations in memory.
 * @param {{ orgs?: Array<any> }} seed
 */
function fakeOrgs(seed) {
  let n = 0
  const next = (p) => `${p}-test${++n}`
  /** @type {Map<string, any>} */
  const orgs = new Map()
  /** @type {Array<{ action: string, org: string, [key: string]: any }>} */
  const changed = []
  const note = (action, org, extra = {}) => void changed.push({ action, org, ...extra })
  const err = (status, code, message) => new FunctionError(status, code, message)
  const record = (o) => ({
    id: o.id, name: o.name, slug: o.slug ?? null, status: o.status, require_mfa: false,
    claims: structuredClone(o.claims), created_at: '2030-01-01T00:00:00.000Z',
  })
  const make = (input) => {
    const o = {
      id: input.id ?? next('org'), name: input.name, slug: input.slug ?? null, status: 'active', claims: input.claims ?? {},
      members: new Map((input.members ?? []).map((m) => [m.user, { roles: [...(m.roles ?? [])], teams: [...(m.teams ?? [])], teamRoles: (m.teamRoles ?? []).map((g) => ({ ...g })) }])),
      teams: new Map((input.teams ?? []).map((t) => { const id = t.id ?? next('team'); return [id, { id, name: t.name, roles: [...(t.roles ?? [])] }] })),
      invitations: new Map(),
    }
    orgs.set(o.id, o)
    return o
  }
  for (const o of seed.orgs ?? []) make(o)
  const need = (id) => {
    const o = orgs.get(id)
    if (!o) throw err(404, 'not_found', 'organization not found')
    return o
  }
  const memberView = (id, user, m) => ({ user, roles: [...m.roles], teams: [...m.teams], team_roles: (m.teamRoles ?? []).map((g) => ({ ...g, granted_at: '2030-01-01T00:00:00.000Z', active: true })), effective_roles: m.roles.map((role) => ({ role, origin: ['direct'] })), overlap: [], joined_at: '2030-01-01T00:00:00.000Z' })
  const team = (o, id) => {
    const t = o.teams.get(id)
    if (!t) throw err(404, 'not_found', 'team not found')
    return t
  }
  const api = {
    create: async (input) => {
      if (!input || typeof input.name !== 'string' || input.name.trim() === '') throw err(400, 'validation_error', 'name is required')
      if (!input.owner) throw err(400, 'validation_error', 'owner is required')
      if (input.slug && [...orgs.values()].some((x) => x.slug === input.slug && x.status !== 'deleted')) throw err(409, 'slug_taken', 'that slug is taken')
      const o = make({ name: input.name.trim(), slug: input.slug, members: [{ user: input.owner, roles: ['owner'] }] })
      note('create', o.id)
      return { ...record(o), roles: ['owner'], effective_roles: [], teams: [] }
    },
    get: async (id) => record(need(id)),
    list: async (options = {}) => [...orgs.values()].filter((o) => !options.status || o.status === options.status).map(record),
    setClaims: async (id, claims) => {
      const o = need(id)
      if (JSON.stringify(claims).length > 1024) throw err(400, 'validation_error', 'claims must be at most 1024 bytes')
      o.claims = structuredClone(claims)
      note('setClaims', id, { claims: structuredClone(claims) })
      return record(o)
    },
    members: async (id) => ({ items: [...need(id).members].map(([u, m]) => memberView(id, u, m)), has_more: false }),
    addMember: async (id, input) => {
      const o = need(id)
      const m = o.members.get(input.user) ?? { roles: [], teams: [] }
      for (const r of input.roles ?? []) if (!m.roles.includes(r)) m.roles.push(r)
      o.members.set(input.user, m)
      note('addMember', id, { user: input.user, roles: [...(input.roles ?? [])] })
      return memberView(id, input.user, m)
    },
    setRoles: async (id, user, roles) => {
      const o = need(id)
      const m = o.members.get(user)
      if (!m) throw err(404, 'not_found', 'that user is not a member of the organization')
      m.roles = [...roles]
      note('setRoles', id, { user, roles: [...roles] })
      return memberView(id, user, m)
    },
    removeMember: async (id, user) => {
      const o = need(id)
      if (!o.members.has(user)) throw err(404, 'not_found', 'that user is not a member of the organization')
      const owners = [...o.members].filter(([u, m]) => u !== user && m.roles.includes('owner'))
      if (o.members.get(user).roles.includes('owner') && owners.length === 0) throw err(409, 'last_org_manager', 'the organization needs at least one member who can manage it')
      o.members.delete(user)
      note('removeMember', id, { user })
    },
    assignOwner: async (id, user) => api.addMember(id, { user, roles: ['owner'] }),
    teams: Object.freeze({
      list: async (id) => [...need(id).teams.values()].map((t) => ({ ...t, roles: [...t.roles], created_at: '2030-01-01T00:00:00.000Z' })),
      create: async (id, input) => {
        const o = need(id)
        if ([...o.teams.values()].some((t) => t.name === input.name)) throw err(409, 'team_name_taken', 'the organization has a team with that name')
        const t = { id: next('team'), name: input.name, roles: [...(input.roles ?? [])] }
        o.teams.set(t.id, t)
        note('teams.create', id, { team: t.id, name: t.name })
        return { ...t, created_at: '2030-01-01T00:00:00.000Z' }
      },
      update: async (id, teamID, patch) => {
        const t = team(need(id), teamID)
        if (patch.name !== undefined) t.name = patch.name
        if (patch.roles !== undefined) t.roles = [...patch.roles]
        note('teams.update', id, { team: teamID })
        return { ...t, created_at: '2030-01-01T00:00:00.000Z' }
      },
      delete: async (id, teamID) => {
        const o = need(id)
        team(o, teamID)
        o.teams.delete(teamID)
        for (const m of o.members.values()) m.teams = m.teams.filter((t) => t !== teamID)
        note('teams.delete', id, { team: teamID })
      },
      members: async (id, teamID, options = {}) => {
        const o = need(id)
        team(o, teamID)
        const items = []
        for (const [user, m] of [...o.members].sort(([a], [b]) => (a < b ? -1 : 1))) {
          const held = (m.teamRoles ?? []).filter((g) => g.team === teamID).map((g) => g.role)
          const inTeam = m.teams.includes(teamID)
          if (options.role ? !held.includes(options.role) : !inTeam && held.length === 0) continue
          items.push({ ...memberView(id, user, m), in_team: inTeam, roles_in_team: held })
        }
        return { items, has_more: false }
      },
      grantRole: async (id, teamID, input) => {
        const o = need(id)
        team(o, teamID)
        const m = o.members.get(input.user)
        if (!m) throw err(404, 'not_found', 'that user is not a member of the organization')
        if (!input.role) throw err(400, 'validation_error', 'role is required')
        m.teamRoles = m.teamRoles ?? []
        if (!m.teamRoles.some((g) => g.team === teamID && g.role === input.role)) {
          m.teamRoles.push({ team: teamID, role: input.role, ...(input.from ? { from: input.from } : {}), ...(input.until ? { until: input.until } : {}) })
        }
        note('teams.grantRole', id, { team: teamID, user: input.user, role: input.role })
        return { ...memberView(id, input.user, m), in_team: m.teams.includes(teamID), roles_in_team: m.teamRoles.filter((g) => g.team === teamID).map((g) => g.role) }
      },
      setRoleWindow: async (id, teamID, input) => {
        const o = need(id)
        team(o, teamID)
        const g = o.members.get(input.user)?.teamRoles?.find((x) => x.team === teamID && x.role === input.role)
        if (!g) throw err(404, 'not_found', 'the member doesn\'t hold that role in the team')
        delete g.from
        delete g.until
        if (input.from) g.from = input.from
        if (input.until) g.until = input.until
        note('teams.setRoleWindow', id, { team: teamID, user: input.user, role: input.role })
        return memberView(id, input.user, o.members.get(input.user))
      },
      revokeRole: async (id, teamID, input) => {
        const o = need(id)
        team(o, teamID)
        const m = o.members.get(input.user)
        if (!m) throw err(404, 'not_found', 'that user is not a member of the organization')
        m.teamRoles = (m.teamRoles ?? []).filter((g) => !(g.team === teamID && g.role === input.role))
        note('teams.revokeRole', id, { team: teamID, user: input.user, role: input.role })
        return memberView(id, input.user, m)
      },
      addMember: async (id, teamID, user) => {
        const o = need(id)
        team(o, teamID)
        const m = o.members.get(user)
        if (!m) throw err(404, 'not_found', 'that user is not a member of the organization')
        if (!m.teams.includes(teamID)) m.teams.push(teamID)
        note('teams.addMember', id, { team: teamID, user })
        return memberView(id, user, m)
      },
      removeMember: async (id, teamID, user) => {
        const o = need(id)
        team(o, teamID)
        const m = o.members.get(user)
        if (!m) throw err(404, 'not_found', 'that user is not a member of the organization')
        m.teams = m.teams.filter((t) => t !== teamID)
        note('teams.removeMember', id, { team: teamID, user })
        return memberView(id, user, m)
      },
    }),
    invitations: Object.freeze({
      create: async (id, input) => {
        const o = need(id)
        if (!['user', 'email', 'none'].includes(input?.binding)) throw err(400, 'validation_error', 'binding must be user, email or none')
        if (input.binding === 'none' && (input.roles ?? []).includes('owner')) throw err(400, 'validation_error', "an open link can't give a role that manages the organization")
        const inv = { id: next('inv'), binding: input.binding, status: 'pending', roles: [...(input.roles ?? [])], teams: [...(input.teams ?? [])], user: input.user, email: input.email }
        o.invitations.set(inv.id, inv)
        note('invitations.create', id, { invitation: inv.id, binding: inv.binding })
        return { ...inv, token: `bdo_test${n}`, link: `https://example.com/join?token=bdo_test${n}&org=${id}` }
      },
      list: async (id) => [...need(id).invitations.values()].map((i) => ({ ...i })),
      delete: async (id, invitation) => {
        const o = need(id)
        if (!o.invitations.delete(invitation)) throw err(404, 'not_found', 'invitation not found')
        note('invitations.delete', id, { invitation })
      },
    }),
  }
  return { api: Object.freeze(api), changes: () => structuredClone(changed) }
}
