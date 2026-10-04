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
 * @property {string} [deleted_at] RFC3339 timestamp; only on soft-deleted documents (collections with `soft_delete`).
 * @property {string} [deleted_by] Who deleted it, like `updated_by`.
 * @property {string} [purge_at]   RFC3339 timestamp: when it is removed for good (`soft_delete.retention`).
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
 * @property {string} [after]               The `next_cursor` of the previous page: continues the list after it
 *   (same `where` and `orderBy`). Can't be combined with `skip`.
 * @property {boolean} [count]              Also return `total`.
 * @property {'only' | 'include'} [deleted] In a collection with `soft_delete`: `'only'` lists the deleted documents
 *   (the trash), `'include'` both. Needs the `restore` rule besides `read`.
 */

/**
 * A page of documents, as the API returns it.
 * @template {object} [T=Record<string, any>]
 * @typedef {object} Page
 * @property {Doc<T>[]} items
 * @property {number} limit
 * @property {number} skip
 * @property {boolean} has_more
 * @property {string} [next_cursor]   With `has_more`: pass it as `after` to get the next page. Absent when the
 *   `orderBy` can't be followed by a cursor (arrays, objects, mixed types): use `skip` then.
 * @property {number} [total]   With `count: true`; counts the whole list, not what is after `after`.
 */

/**
 * Options for writes. `ifMatch` makes the write fail with a
 * VersionMismatchError unless the document is still at that version.
 * @typedef {RequestOptions & { ifMatch?: number | string }} WriteOptions
 */

/**
 * Options for `get`. `deleted` reads a soft-deleted document (see `ListParams`).
 * @typedef {RequestOptions & { deleted?: 'only' | 'include' }} GetOptions
 */

/**
 * Options for `delete`. `purge` removes the document for good instead of
 * soft-deleting it (a collection with `soft_delete`, under the `purge` rule).
 * @typedef {WriteOptions & { purge?: boolean }} DeleteOptions
 */

/**
 * Options for creates and batches. `idempotencyKey` makes a retry safe: the
 * server remembers the answer of the first request with a key for 24 hours and
 * returns it again for the same key and body, instead of creating a second
 * document. The client also retries such a request itself after a network
 * error, `429` or `503` (see `retry`).
 * @typedef {RequestOptions & { idempotencyKey?: string }} CreateOptions
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
    /** @internal */
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
   * @param {CreateOptions} [opts]
   * @returns {Promise<(Doc | { id: string })[]>} one entry per operation, in order
   */
  async batch(operations, opts) {
    const body = {
      operations: operations.map(({ ifMatch, ...op }) => (ifMatch === undefined ? op : { ...op, if_match: ifMatchValue(ifMatch) })),
    }
    const { data } = await this.client.request({ method: 'POST', path: [this.name, '_batch'], body, ...keyed(opts) })
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
   * With `respondAsync: true` (`Prefer: respond-async`) a `sync` function
   * is queued as a job too, and this resolves with its `Job` instead of the
   * output: for calls that can take long, or that nobody waits for. It
   * runs once, with the function's own limits.
   *
   * `webhook` functions can't be called through this method: their
   * caller is whatever service sends the webhook, never this client.
   * @param {string} name
   * @param {unknown} [input] Any JSON value; omitted is sent as `null`.
   * @param {RequestOptions & { idempotencyKey?: string, respondAsync?: boolean }} [opts]
   * @returns {Promise<unknown | Job>}
   */
  async fn(name, input, opts = {}) {
    const { idempotencyKey, respondAsync, headers, ...rest } = opts
    const reqHeaders = { ...headers }
    if (idempotencyKey !== undefined) reqHeaders['Idempotency-Key'] = idempotencyKey
    if (respondAsync) reqHeaders.Prefer = 'respond-async'
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
    /** @internal */
    this.client = client
    /** @internal */
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
   * `for await (const doc of posts.iterate({ where }))`. Pages follow a
   * cursor (`next_cursor`), so documents created, changed or deleted
   * meanwhile never shift a page, and it is as fast at the end as at the
   * start. An `orderBy` on arrays, objects or mixed types has no cursor:
   * those lists are fetched by offset instead, where documents created or
   * deleted meanwhile can be skipped or repeated.
   * @param {Omit<ListParams, 'skip' | 'count'>} [params] `limit` is the page size (default 100).
   * @param {RequestOptions} [opts]
   * @returns {AsyncGenerator<Doc<T>, void, undefined>}
   */
  async *iterate(params = {}, opts) {
    const limit = params.limit ?? 100
    let after = params.after
    for (let skip = 0; ; skip += limit) {
      // With a cursor the next request carries it; without one, the offset.
      const page = await this.list(after === undefined ? { ...params, limit, skip } : { ...params, limit, after }, opts)
      yield* page.items
      if (!page.has_more) return
      if (page.next_cursor === undefined) {
        if (after !== undefined) throw new Error('backd: the list has no next_cursor to continue after')
      } else {
        after = page.next_cursor
      }
    }
  }

  /**
   * One document; NotFoundError if it doesn't exist or the caller may not read it.
   * A soft-deleted document is only found with `deleted: 'only'` or `'include'`.
   * @param {string} id
   * @param {GetOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async get(id, opts = {}) {
    const { deleted, ...rest } = opts
    return (await this.client.request({ method: 'GET', path: [...this.path, id], query: { deleted }, ...rest })).data
  }

  /**
   * Creates a document. `id` and `_meta` are set by the server. With an
   * `idempotencyKey`, a repeated request returns the document created by the
   * first one.
   * @param {T} doc
   * @param {CreateOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async create(doc, opts) {
    return (await this.client.request({ method: 'POST', path: this.path, body: doc, ...keyed(opts) })).data
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
   * Deletes a document. In a collection with `soft_delete` it is marked deleted
   * and can be restored; `purge: true` removes it for good.
   * @param {string} id
   * @param {DeleteOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(id, opts = {}) {
    const { purge, ...rest } = opts
    await this.client.request({ method: 'DELETE', path: [...this.path, id], query: { purge: purge || undefined }, ...write(rest) })
  }

  /**
   * Brings a soft-deleted document back, as it was, at the next version.
   * NotFoundError if it isn't in the trash or the caller may not see it there.
   * @param {string} id
   * @param {WriteOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async restore(id, opts = {}) {
    return (await this.client.request({ method: 'POST', path: [...this.path, id, 'restore'], ...write(opts) })).data
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
    after: p.after,
    count: p.count || undefined,
    deleted: p.deleted,
  }
}

/**
 * Turns `idempotencyKey` into an Idempotency-Key header.
 * @param {CreateOptions} [opts]
 * @returns {RequestOptions}
 */
function keyed(opts = {}) {
  const { idempotencyKey, ...rest } = opts
  return idempotencyKey === undefined ? rest : { ...rest, headers: { ...rest.headers, 'Idempotency-Key': idempotencyKey } }
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
