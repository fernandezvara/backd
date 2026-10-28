import { Files, downloadFile, fileField, fileUrl, prepareUpload, uploadFile } from './files.js'
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
 * @property {string} [org]        The organization the document belongs to (collections with `organization`).
 * @property {string} [team]       The team of the organization the document belongs to (collections with `team`).
 * @property {number} [score]      The relevance to a `$search`, in a list sorted with `orderBy: '_relevance'`. Never stored.
 * @property {string} [deleted_at] RFC3339 timestamp; only on soft-deleted documents (collections with `soft_delete`).
 * @property {string} [deleted_by] Who deleted it, like `updated_by`.
 * @property {string} [purge_at]   RFC3339 timestamp: when it is removed for good (`soft_delete.retention`).
 * @property {string[]} [redacted]  The encrypted fields the caller may not see (their `reveal` rule said no): they come back `null`, and a `null` here is hidden, not empty. Absent when nothing is hidden.
 * @property {{ root: string, depth: number }} [chain]  Set on a write made by a trigger run: where it sits in a chain of such writes.
 */

/**
 * A stored document: the collection's fields plus `id` and `_meta`.
 * @template {object} [T=Record<string, any>]
 * @typedef {T & { id: string, _meta: Meta, _expanded?: Record<string, any> }} Doc
 */

/**
 * What a reference expanded to (`_expanded.<name>`, only on a read with `expand`): the referenced
 * document (only the selected fields and `id`, when the request selected some), `null` when it is
 * missing, unreadable for the caller or in another organization, an array of those for a `many`
 * reference (positions kept), or `{ items, truncated: true }` when the array was longer than the
 * server's limit. A second level is in the expanded document's own `_expanded`.
 * An untyped client leaves `_expanded` as a record of `any`: type the client from the generated
 * `Realm` for these shapes per reference.
 * @typedef {Doc | null | (Doc | null)[] | { items: (Doc | null)[], truncated: true }} Expanded
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
 * @property {boolean} [fileLinks]          Add `url` and `expires_at` to every file of every document.
 * @property {string} [language]            The language a `$search` is stemmed in (a locale code such as `es`
 *   or `en-GB`); default: the user's locale, then the browser's.
 * @property {string} [expand]              References to return in `_expanded`: `'author(name),tags'`, or built
 *   with `expand(['author', ['name']], 'tags')`. Never sent unless you set it.
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
 * `fileLinks` adds `url` and `expires_at` to every file the document holds: for content shown right
 * away (a gallery, a preview). They expire; for a download button, `downloadFile` gets a fresh link.
 * `expand` returns the declared references named in it in `_expanded` (see `expand()`).
 * @typedef {RequestOptions & { deleted?: 'only' | 'include', fileLinks?: boolean, expand?: string }} GetOptions
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
 * `team` is the team of the session's organization a document of a collection with `team` is
 * created in (a member, or a holder of a team role there; `?team=` on the wire). A session works
 * in the organization it was opened for, so there is no organization to name. `org` is for the
 * administrators' data route (`admin.data(...)`), which creates in any active organization.
 * @typedef {RequestOptions & { idempotencyKey?: string, team?: string, org?: string }} CreateOptions
 */

/**
 * One write for `db.batch(...)`, applied atomically with the others.
 * `create` and `replace` need `document`; `patch` needs `patch` (a JSON
 * Merge Patch). `replace`, `patch` and `delete` need `id`, and accept
 * `ifMatch` (like `WriteOptions`). A `create` in a collection with
 * `custom_id` carries the new document's `id`.
 * @typedef {object} BatchOperation
 * @property {'create' | 'replace' | 'patch' | 'delete'} op
 * @property {string} collection
 * @property {string} [id]
 * @property {Record<string, any>} [document]
 * @property {Record<string, any>} [patch]
 * @property {number | string} [ifMatch]
 */

/**
 * What to summarize: fields declared in the collection's `aggregate:`.
 * @typedef {object} AggregateParams
 * @property {object | string} [where]  The same filter as a list's, with `$search`; the read rules apply first.
 * @property {string | string[]} [groupBy]  Up to 3 declared fields; a date takes a bucket: `'paid_at:month'`
 *   (`day`, `week`, `month`, `year`). Without it, one group with an empty key (a counter).
 * @property {string | string[]} [metrics]  `count` (the default), `count_distinct:<field>`, `sum:<field>`,
 *   `avg:<field>`, `min:<field>`, `max:<field>`.
 * @property {string} [tz]              IANA zone of the date buckets (`Europe/Madrid`); default `UTC`.
 * @property {string | string[]} [orderBy]  Group keys and result names, `-` for descending: `'-count'`.
 * @property {number} [limit]           Groups returned (default 100, at most the collection's maximum).
 * @property {string} [language]        Language of a `$search` in `where`.
 */

/**
 * One group: `key` has an entry per `groupBy` field (`null` for documents without a value), and the
 * metrics are named `<metric>_<field>`: `count`, `sum_total`, `avg_total`, `count_distinct__meta.owner`.
 * Sums and averages are exact decimal strings.
 * @typedef {{ key: Record<string, string | number | boolean | null>, count?: number, [metric: string]: any }} AggregateGroup
 */

/**
 * @typedef {object} AggregateResult
 * @property {AggregateGroup[]} groups
 * @property {number} total_groups  How many groups there were before `limit`.
 */

/**
 * Options of `search`: a `list` with the text as its `$search`, best match first.
 * @typedef {Omit<ListParams, 'orderBy' | 'after'> & { orderBy?: never }} SearchParams
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
   * @param {string} [team] The team lists and creates are scoped to (see {@link Collection#inTeam}).
   */
  constructor(client, database, name, team) {
    /** @internal */
    this.client = client
    /** @internal */
    this.path = [database, name]
    /** @internal */
    this.team = team
  }

  /**
   * The same collection scoped to a team of the session's organization: `create` puts documents
   * in that team and `list` and `iterate` show only its documents (the read rule still decides
   * what is seen). The caller must belong to the team or hold a team role in it: a create answers
   * `NotTeamMemberError` otherwise.
   * @param {string} team
   * @returns {Collection<T>}
   */
  inTeam(team) {
    return new Collection(this.client, /** @type {string} */ (this.path[0]), /** @type {string} */ (this.path[1]), team)
  }

  /**
   * Follows this collection live (see `client.realtime.subscribe`): a list that loads first, then
   * keeps itself current. In a team scope (`inTeam`) the subscription filters on it.
   * @param {Omit<import('./realtime.js').SubscriptionSpec, 'db' | 'collection'> & import('./realtime.js').SubscribeOptions} [spec]
   * @returns {import('./realtime.js').LiveView}
   */
  subscribe(spec = {}) {
    const { jitterMs, random, maxBackoffMs, ...rest } = spec
    /** @type {Record<string, any>} */
    const where = { ...(typeof rest.where === 'object' ? rest.where : {}) }
    if (this.team !== undefined) where['_meta.team'] = this.team
    return this.client.realtime.subscribe(
      [{ ...rest, db: this.path[0], collection: this.path[1], where: Object.keys(where).length ? where : undefined }],
      { jitterMs, random, maxBackoffMs },
    )
  }

  /**
   * One page of documents the caller may read.
   * @param {ListParams} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<Page<T>>}
   */
  async list(params = {}, opts) {
    const query = { ...listQuery(params), ...scopeQuery(this.team) }
    const { data } = await this.client.request({ method: 'GET', path: this.path, query, ...opts })
    return data
  }

  /**
   * Summarizes the documents the caller may read: counts and measures grouped by the fields the
   * collection declares in `aggregate:`. The read rules apply before grouping, so a total never
   * includes a document a list would hide.
   *
   * ```js
   * const { groups } = await orders.aggregate({
   *   groupBy: ['category', 'paid_at:month'], tz: 'Europe/Madrid',
   *   metrics: ['count', 'sum:total'], orderBy: '-sum_total', limit: 20,
   * })
   * groups[0].key          // { category: 'shoes', 'paid_at:month': '2026-09' }
   * groups[0].sum_total    // '18234.5' (an exact decimal string)
   * ```
   *
   * `AggregateNotEnabledError` when the collection declares no `aggregate:`, `InvalidAggregateError`
   * for a wrong parameter and `AggregateTooExpensiveError` when it needs more than allowed.
   * @param {AggregateParams} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<AggregateResult>}
   */
  async aggregate(params = {}, opts) {
    const list = (/** @type {string | string[] | undefined} */ v) => (Array.isArray(v) ? v.join(',') : v)
    const query = {
      where: params.where === undefined ? undefined : typeof params.where === 'string' ? params.where : JSON.stringify(params.where),
      group_by: list(params.groupBy),
      metrics: list(params.metrics),
      tz: params.tz,
      order_by: list(params.orderBy),
      limit: params.limit,
      language: params.language,
      ...scopeQuery(this.team),
    }
    const { data } = await this.client.request({ method: 'GET', path: [...this.path, '_aggregate'], query, ...opts })
    return data
  }

  /**
   * Full-text search, best match first: a `list` whose `where` carries `text` as `$search`
   * and whose order is `_relevance`, with each document's score in `_meta.score`. `text` is
   * words, `"quoted phrases"` and `-excluded` words. Pages with `skip`, through the first 1,000
   * results. `SearchNotEnabledError` when the collection has no text index.
   *
   * ```js
   * const page = await articles.search('red "running shoes" -kids', { where: { status: 'published' }, limit: 10 })
   * ```
   * @param {string} text
   * @param {SearchParams} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<Page<T>>}
   */
  async search(text, params = {}, opts) {
    if (typeof text !== 'string' || text.trim() === '') throw new TypeError('backd: search needs the text to search for')
    const base = typeof params.where === 'string' ? JSON.parse(params.where) : { ...params.where }
    return this.list({ ...params, where: { ...base, $search: text }, orderBy: '_relevance' }, opts)
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
    const { deleted, fileLinks, expand, ...rest } = opts
    return (await this.client.request({ method: 'GET', path: [...this.path, id], query: { deleted, file_links: fileLinks || undefined, expand }, ...rest })).data
  }

  /**
   * Creates a document. `_meta` is set by the server, and so is `id`, unless the collection declares
   * `custom_id`: then `doc` carries the `id` (`400 id_required` otherwise; `409 id_taken` if it
   * exists). With an
   * `idempotencyKey`, a repeated request returns the document created by the
   * first one.
   * @param {T} doc
   * @param {CreateOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async create(doc, opts) {
    const scoped = { ...opts }
    if (scoped.team === undefined && this.team !== undefined) scoped.team = this.team
    return (await this.client.request({ method: 'POST', path: this.path, body: doc, ...keyed(scoped) })).data
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
   * The files a document holds in a file field (`files:` in collection.yaml): `get`, `put`,
   * `delete` and `link`. They are the `_files` routes of the API, under the caller's own
   * rules (the `update` rule for `put` and `delete`, `read` for `get` and `link`); in a
   * function, `ctx.admin.db` skips them.
   * @param {string} id        The document.
   * @param {string} field     The file field.
   * @returns {Files}
   */
  files(id, field) {
    return new Files(this.client, this.path, id, field)
  }

  /**
   * How a file field takes files: `upload` (`proxy` or `direct`), `max_size`, `types`, `multiple`
   * and `max_files`. Asked once and remembered.
   * @param {string} field
   * @param {RequestOptions} [opts]
   * @returns {Promise<import('./files.js').FileField>}
   */
  fileField(field, opts) {
    return fileField(this.client, this.path, field, opts)
  }

  /**
   * Uploads a file into a document's file field, whichever way the field takes it (through
   * backd, or straight to the bucket), reporting the bytes sent to `onProgress`. Resolves with
   * the updated document.
   * @param {string} id
   * @param {string} field
   * @param {Blob | File | Uint8Array | ArrayBuffer | string} file
   * @param {import('./files.js').UploadOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  uploadFile(id, field, file, opts) {
    return /** @type {Promise<Doc<T>>} */ (uploadFile(this.client, this.path, id, field, file, opts))
  }

  /**
   * Uploads a file for a document that doesn't exist yet. Name the result in the field when
   * creating it: `create({ title, receipts: [up.ref] })` (`up.ref` is `{ upload, token }`).
   * @param {string} field
   * @param {Blob | File | Uint8Array | ArrayBuffer | string} file
   * @param {import('./files.js').UploadOptions} [opts]
   * @returns {Promise<import('./files.js').PreparedUpload>}
   */
  prepareUpload(field, file, opts) {
    return prepareUpload(this.client, this.path, field, file, opts)
  }

  /**
   * A link to a file, to show or open: `{ url, expiresAt }`. It works without credentials until
   * it expires, so fetch it when it is needed. With `version` it links that made version of an
   * image (a thumbnail), or is `null` while the version isn't ready. Pass the file's details as `file` and the address of a public file is answered without a request (`expiresAt` is `null`: it never expires).
   * @overload
   * @param {string} id
   * @param {string} field
   * @param {string} fileId
   * @param {RequestOptions & { version?: undefined, file?: import('./files.js').FileDetails }} [opts]
   * @returns {Promise<{ url: string, expiresAt: string | null }>}
   */
  /**
   * @overload
   * @param {string} id
   * @param {string} field
   * @param {string} fileId
   * @param {RequestOptions & { version: string, file?: import('./files.js').FileDetails }} opts
   * @returns {Promise<{ url: string, expiresAt: string | null } | null>}
   */
  /**
   * @param {string} id
   * @param {string} field
   * @param {string} fileId
   * @param {import('./files.js').VersionOptions} [opts]
   */
  fileUrl(id, field, fileId, opts) {
    return fileUrl(this.client, this.path, id, field, fileId, /** @type {any} */ (opts))
  }

  /**
   * Starts a download in the browser: a fresh link, opened with `location.assign`; the browser
   * saves the file and stays on the page. Throws outside a browser: use `fileUrl`.
   * @param {string} id
   * @param {string} field
   * @param {string} fileId
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ url: string, expiresAt: string | null }>}
   */
  downloadFile(id, field, fileId, opts) {
    return downloadFile(this.client, this.path, id, field, fileId, opts)
  }

  /**
   * Removes one file from a document. Resolves with the updated document.
   * @param {string} id
   * @param {string} field
   * @param {string} fileId
   * @param {WriteOptions} [opts]
   * @returns {Promise<Doc<T>>}
   */
  async deleteFile(id, field, fileId, opts) {
    if (typeof fileId !== 'string' || fileId === '') throw new TypeError('deleteFile: the id of the file to remove is required (files(id, field).delete() clears the field)')
    return /** @type {Promise<Doc<T>>} */ (this.files(id, field).delete(fileId, opts))
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
    file_links: p.fileLinks || undefined,
    expand: p.expand,
    language: p.language,
  }
}

/**
 * The `team` query parameter of a scope, when it is set.
 * @param {string} [team]
 * @returns {Record<string, string>}
 */
function scopeQuery(team) {
  /** @type {Record<string, string>} */
  const q = {}
  if (team !== undefined) q.team = team
  return q
}

/**
 * Turns `idempotencyKey` into an Idempotency-Key header.
 * @param {CreateOptions} [opts]
 * @returns {RequestOptions & { query?: Record<string, string> }}
 */
function keyed(opts = {}) {
  const { idempotencyKey, team, org, ...rest } = opts
  const scope = { ...scopeQuery(team), ...(org === undefined ? {} : { org }) }
  /** @type {RequestOptions & { query?: Record<string, string> }} */
  const out = Object.keys(scope).length === 0 ? rest : { ...rest, query: scope }
  return idempotencyKey === undefined ? out : { ...out, headers: { ...out.headers, 'Idempotency-Key': idempotencyKey } }
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
