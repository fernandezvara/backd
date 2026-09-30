import { Job } from './functions.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./functions.js').JobData} JobData
 */

/**
 * Server-owned fields of a document.
 * @typedef {object} Meta
 * @property {string} created_at   RFC3339 timestamp.
 * @property {string} updated_at   RFC3339 timestamp.
 * @property {number} [version]    Increases on every write; missing only on very old documents.
 * @property {string | null} [owner]  Id of the user who created it (realms with auth enabled).
 * @property {string} [created_by] `user:<id>`, `key:<name>` or `anonymous`.
 * @property {string} [updated_by] `user:<id>`, `key:<name>` or `anonymous`.
 */

/**
 * A stored document: the collection's fields plus `id` and `_meta`.
 * @template {object} [T=Record<string, any>]
 * @typedef {T & { id: string, _meta: Meta }} Doc
 */

/**
 * @typedef {object} ListParams
 * @property {object | string} [where]      Filter in backd's query language, e.g. `{ price: { $lt: 20 } }`
 *   or `{ $or: [{ title: { $icontains: 'go' } }, { body: { $icontains: 'go' } }] }`.
 * @property {string | string[]} [orderBy]  Fields, `-` prefix for descending, e.g. `'-_meta.created_at'`.
 * @property {number} [limit]               1–100; default 20.
 * @property {number} [skip]
 * @property {boolean} [count]              Also return `total`.
 */

/**
 * A page of documents, as the API returns it.
 * @template {object} [T=Record<string, any>]
 * @typedef {object} Page
 * @property {Doc<T>[]} items
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 * @property {number} [total]   With `count: true`.
 */

/**
 * Options for writes. `ifMatch` makes the write fail with a
 * VersionMismatchError unless the document is still at that version.
 * @typedef {RequestOptions & { ifMatch?: number | string }} WriteOptions
 */

/**
 * One write for `db.batch(...)`, applied atomically with the others.
 * `create` and `replace` need `document`; `patch` needs `patch` (a JSON
 * Merge Patch). `replace`, `patch` and `delete` need `id`, and accept
 * `ifMatch` (like `WriteOptions`).
 * @typedef {object} BatchOperation
 * @property {'create' | 'replace' | 'patch' | 'delete'} op
 * @property {string} collection
 * @property {string} [id]
 * @property {Record<string, any>} [document]
 * @property {Record<string, any>} [patch]
 * @property {number | string} [ifMatch]
 */

/** A database of the realm: `client.db(name)`. */
export class Database {
  /**
   * @param {Client} client
   * @param {string} name
   */
  constructor(client, name) {
    /** @private */
    this.client = client
    /** @readonly */
    this.name = name
  }

  /**
   * A collection of this database.
   * @template {object} [T=Record<string, any>]
   * @param {string} name
   * @returns {Collection<T>}
   */
  collection(name) {
    return new Collection(this.client, this.name, name)
  }

  /**
   * Creates, replaces, patches and deletes documents across this
   * database's collections in one MongoDB transaction: either every
   * operation applies, or none does (up to 100 operations). Rejects with
   * a `BackdError` naming the first operation that failed; a version
   * mismatch (`ifMatch`) is a `VersionMismatchError`, same as a single
   * write.
   * @param {BatchOperation[]} operations
   * @param {RequestOptions} [opts]
   * @returns {Promise<(Doc | { id: string })[]>} one entry per operation, in order
   */
  async batch(operations, opts) {
    const body = {
      operations: operations.map(({ ifMatch, ...op }) => (ifMatch === undefined ? op : { ...op, if_match: ifMatchValue(ifMatch) })),
    }
    const { data } = await this.client.request({ method: 'POST', path: [this.name, '_batch'], body, ...opts })
    return data.results
  }

  /**
   * Calls a function. Returns its output directly for a `sync`
   * function; for `async`, a `Job` handle to poll instead of the
   * output (`job.status()`, or `job.wait()` for the output). A
   * function's own error (`ctx.error(status, code, message)`) arrives
   * as a `BackdError` with that status and code — for `async`, only
   * once `job.wait()` resolves, not from this call itself.
   *
   * `webhook` functions can't be called through this method: their
   * caller is whatever service sends the webhook, never this client.
   * @param {string} name
   * @param {unknown} [input] Any JSON value; omitted is sent as `null`.
   * @param {RequestOptions & { idempotencyKey?: string }} [opts]
   * @returns {Promise<unknown | Job>}
   */
  async fn(name, input, opts = {}) {
    const { idempotencyKey, headers, ...rest } = opts
    const reqHeaders = idempotencyKey === undefined ? headers : { ...headers, 'Idempotency-Key': idempotencyKey }
    const { status, data } = await this.client.request({
      method: 'POST',
      path: [this.name, '_func', name],
      body: input === undefined ? null : input,
      headers: reqHeaders,
      ...rest,
    })
    if (status === 202) return new Job(this.client, this.name, /** @type {JobData} */ (data))
    return data
  }
}

/**
 * A collection: `client.db(db).collection(name)`. `T` describes the
 * documents' own fields.
 * @template {object} [T=Record<string, any>]
 */
export class Collection {
  /**
   * @param {Client} client
   * @param {string} database
   * @param {string} name
   */
  constructor(client, database, name) {
    /** @private */
    this.client = client
    /** @private */
    this.path = [database, name]
  }

  /**
   * One page of documents the caller may read.
   * @param {ListParams} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<Page<T>>}
   */
  async list(params = {}, opts) {
    const { data } = await this.client.request({ method: 'GET', path: this.path, query: listQuery(params), ...opts })
    return data
  }

  /**
   * Every matching document, fetching pages as needed:
   * `for await (const doc of posts.iterate({ where }))`. Pages are
   * fetched by offset, so documents created or deleted meanwhile can be
   * skipped or repeated.
   * @param {Omit<ListParams, 'skip' | 'count'>} [params] `limit` is the page size (default 100).
   * @param {RequestOptions} [opts]
   * @returns {AsyncGenerator<Doc<T>, void, undefined>}
   */
  async *iterate(params = {}, opts) {
    const limit = params.limit ?? 100
    for (let skip = 0; ; skip += limit) {
      const page = await this.list({ ...params, limit, skip }, opts)
      yield* page.items
      if (!page.has_more) return
    }
  }

  /**
   * One document; NotFoundError if it doesn't exist or the caller may not read it.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async get(id, opts) {
    return (await this.client.request({ method: 'GET', path: [...this.path, id], ...opts })).data
  }

  /**
   * Creates a document. `id` and `_meta` are set by the server.
   * @param {T} doc
   * @param {RequestOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async create(doc, opts) {
    return (await this.client.request({ method: 'POST', path: this.path, body: doc, ...opts })).data
  }

  /**
   * Replaces the whole document: fields not in `doc` are removed.
   * @param {string} id
   * @param {T} doc
   * @param {WriteOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async replace(id, doc, opts = {}) {
    return (await this.client.request({ method: 'PUT', path: [...this.path, id], body: doc, ...write(opts) })).data
  }

  /**
   * Updates some fields (JSON Merge Patch): fields in `patch` are set,
   * nested objects are merged, and `null` removes a field.
   * @param {string} id
   * @param {Partial<T> | Record<string, unknown>} patch
   * @param {WriteOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async patch(id, patch, opts = {}) {
    return (
      await this.client.request({
        method: 'PATCH',
        path: [...this.path, id],
        body: patch,
        contentType: 'application/merge-patch+json',
        ...write(opts),
      })
    ).data
  }

  /**
   * Deletes a document.
   * @param {string} id
   * @param {WriteOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(id, opts = {}) {
    await this.client.request({ method: 'DELETE', path: [...this.path, id], ...write(opts) })
  }
}

/**
 * @param {ListParams} p
 * @returns {Record<string, string | number | boolean | undefined>}
 */
function listQuery(p) {
  return {
    where: p.where === undefined ? undefined : typeof p.where === 'string' ? p.where : JSON.stringify(p.where),
    order_by: Array.isArray(p.orderBy) ? p.orderBy.join(',') : p.orderBy,
    limit: p.limit,
    skip: p.skip,
    count: p.count || undefined,
  }
}

/**
 * Turns `ifMatch` into an If-Match header.
 * @param {WriteOptions} opts
 * @returns {RequestOptions}
 */
function write({ ifMatch, ...opts }) {
  if (ifMatch === undefined) return opts
  return { ...opts, headers: { ...opts.headers, 'If-Match': ifMatchValue(ifMatch) } }
}

/**
 * `3` → `"3"`; strings (`*`, `"3"`, `"2", "3"`) are sent as given.
 * @param {number | string} v
 */
export function ifMatchValue(v) {
  return typeof v === 'number' ? `"${v}"` : v
}
