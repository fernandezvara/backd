import { NotFoundError } from './errors.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./data.js').WriteOptions} WriteOptions
 */

/**
 * What backd keeps of a file in a document's file field.
 * @typedef {object} FileDetails
 * @property {string} id            `fl_…`.
 * @property {string} name          The sanitized display name.
 * @property {number} size          Bytes.
 * @property {string} type          Detected from the content.
 * @property {string} sha256        Hex.
 * @property {string} uploaded_at   RFC3339 timestamp.
 */

/**
 * A link to a file that works without credentials until it expires.
 * @typedef {object} FileLink
 * @property {string} url
 * @property {string} expires_at   RFC3339 timestamp.
 */

/**
 * Options of `put`: the file's `name` (sanitized by backd; default `file`) and `type`
 * (a hint: backd detects it from the content), and `ifMatch` as in any write.
 * @typedef {WriteOptions & { name?: string, type?: string }} PutOptions
 */

/**
 * A downloaded file: the answer of `get`, not yet read.
 * @typedef {object} FileContent
 * @property {FileDetails} [file]   Its details, when `get` looked them up (no `fileId` given).
 * @property {Response} response    The answer; `response.body` is the stream.
 * @property {() => Promise<Uint8Array>} bytes
 * @property {() => Promise<string>} text
 */

/**
 * The files of one document's file field: `client.db(db).collection(name).files(id, field)`.
 * Every operation is a request to the field's `_files` routes, so the caller's access rules
 * apply as they do to any request.
 */
export class Files {
  /**
   * @param {Client} client
   * @param {string[]} collectionPath  The collection's path after /v1/{realm}.
   * @param {string} id
   * @param {string} field
   */
  constructor(client, collectionPath, id, field) {
    /** @internal */
    this.client = client
    /** @internal */
    this.collectionPath = collectionPath
    /** @internal */
    this.id = id
    /** @internal */
    this.field = field
  }

  /** @internal */
  get path() {
    return [...this.collectionPath, this.id, '_files', this.field]
  }

  /**
   * The files the document holds in the field (one at most, unless the field is `multiple`).
   * NotFoundError if the document doesn't exist or the caller may not read it.
   * @param {RequestOptions} [opts]
   * @returns {Promise<FileDetails[]>}
   */
  async list(opts) {
    const { data } = await this.client.request({ method: 'GET', path: [...this.collectionPath, this.id], ...opts })
    const v = data?.[this.field]
    return v == null ? [] : Array.isArray(v) ? v : [v]
  }

  /** @param {string | undefined} fileId @param {RequestOptions | undefined} opts */
  async #which(fileId, opts) {
    if (fileId !== undefined) return { fileId, file: undefined }
    const files = await this.list(opts)
    if (files.length === 0) throw new NotFoundError({ status: 404, code: 'not_found', message: `the document holds no file in ${this.field}` })
    if (files.length > 1) throw new TypeError(`files(${this.field}): the field holds several files: say which one (a fileId)`)
    return { fileId: files[0].id, file: files[0] }
  }

  /**
   * Downloads a file. Without `fileId` the one a single field holds. NotFoundError when
   * there is none or the caller may not read the document; a `BackdError` with code
   * `file_missing` when the storage no longer has it.
   * @param {string} [fileId]
   * @param {RequestOptions} [opts]
   * @returns {Promise<FileContent>}
   */
  async get(fileId, opts) {
    const found = await this.#which(fileId, opts)
    const { response } = await this.client.request({ method: 'GET', path: [...this.path, found.fileId], raw: true, ...opts })
    if (!response) throw new Error('backd: no response')
    return {
      file: found.file,
      response,
      bytes: async () => new Uint8Array(await response.arrayBuffer()),
      text: () => response.text(),
    }
  }

  /**
   * Stores a file in the field: it replaces the one a single field holds and is added to a
   * `multiple` field's. Resolves with the updated document, which holds the file's details.
   * backd checks the document's `update` rule, the field's `max_size`, `types` and
   * `max_files`, and detects the type from the content. `data` is a string, bytes, a Blob or
   * a stream.
   * @param {string | Uint8Array | ArrayBuffer | Blob | ReadableStream} data
   * @param {PutOptions} [opts]
   * @returns {Promise<import('./data.js').Doc>}
   */
  async put(data, opts = {}) {
    const { name, type, ifMatch, ...rest } = opts
    const contentType = type ?? (typeof Blob !== 'undefined' && data instanceof Blob && data.type ? data.type : 'application/octet-stream')
    const headers = { ...rest.headers }
    if (ifMatch !== undefined) headers['If-Match'] = typeof ifMatch === 'number' ? `"${ifMatch}"` : ifMatch
    const body = typeof data === 'string' ? new TextEncoder().encode(data) : /** @type {BodyInit} */ (data)
    return (
      await this.client.request({ method: 'POST', path: this.path, query: { name }, rawBody: body, contentType, ...rest, headers })
    ).data
  }

  /**
   * Removes a file from the document (a new version), or, without `fileId`, every file of the
   * field. Resolves with the updated document.
   * @param {string} [fileId]
   * @param {WriteOptions} [opts]
   * @returns {Promise<import('./data.js').Doc>}
   */
  async delete(fileId, opts = {}) {
    const { ifMatch, ...rest } = opts
    const headers = { ...rest.headers }
    if (ifMatch !== undefined) headers['If-Match'] = typeof ifMatch === 'number' ? `"${ifMatch}"` : ifMatch
    const path = fileId === undefined ? this.path : [...this.path, fileId]
    return (await this.client.request({ method: 'DELETE', path, ...rest, headers })).data
  }

  /**
   * A link to download the file without credentials, until it expires: for an app to open or
   * show (`{ url, expires_at }`). Without `fileId` the one a single field holds.
   * @param {string} [fileId]
   * @param {RequestOptions} [opts]
   * @returns {Promise<FileLink>}
   */
  async link(fileId, opts) {
    const found = await this.#which(fileId, opts)
    return (await this.client.request({ method: 'GET', path: [...this.path, found.fileId], query: { link: 'json' }, ...opts })).data
  }
}
