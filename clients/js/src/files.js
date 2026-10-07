import { BackdError, NetworkError, NotFoundError } from './errors.js'
import { sha256Blob } from './sha256.js'

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
 * @property {number} [width]       Pixels, for an image (as a viewer sees it).
 * @property {number} [height]
 * @property {Record<string, VersionState>} [versions]   The state of each version the field declares (`versions:` in collection.yaml).
 */

/**
 * One version of an image: `pending` (a worker will make it), `ready` (made: `id`, `size`,
 * `type`, `width`, `height`), `empty` (only functions make it, none has yet), `skipped`
 * (`reason` is `not_an_image` or `unsupported_format`) or `failed` (`reason` is `too_large`
 * or `decode_error`).
 * @typedef {object} VersionState
 * @property {'pending' | 'ready' | 'empty' | 'skipped' | 'failed'} status
 * @property {string} [reason]
 * @property {string} [id]            `fv_…`, once ready.
 * @property {number} [size]          Bytes, once ready.
 * @property {string} [type]
 * @property {number} [width]
 * @property {number} [height]
 * @property {Record<string, unknown>} [params]   What it was made with.
 * @property {string} [fingerprint]
 * @property {string} [generated_at]  RFC3339 timestamp.
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
 * `onProgress` is called as the bytes are sent (see `UploadProgress`).
 * @typedef {WriteOptions & { name?: string, type?: string, onProgress?: (p: import('./client.js').UploadProgress) => void }} PutOptions
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
    const { name, type, ifMatch, onProgress, ...rest } = opts
    const contentType = type ?? (typeof Blob !== 'undefined' && data instanceof Blob && data.type ? data.type : 'application/octet-stream')
    const headers = { ...rest.headers }
    if (ifMatch !== undefined) headers['If-Match'] = typeof ifMatch === 'number' ? `"${ifMatch}"` : ifMatch
    const body = typeof data === 'string' ? new TextEncoder().encode(data) : /** @type {BodyInit} */ (data)
    return (
      await this.client.request({ method: 'POST', path: this.path, query: { name }, rawBody: body, contentType, onUploadProgress: onProgress, ...rest, headers })
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

/**
 * How a file field takes files (`GET …/_files/{field}`): to choose between a proxy and a
 * direct upload, and to refuse a file before sending it.
 * @typedef {object} FileField
 * @property {string} name
 * @property {'proxy' | 'direct'} upload
 * @property {'presigned' | 'proxy'} download
 * @property {boolean} multiple
 * @property {number} max_files   1 for a field that holds one file.
 * @property {number} max_size    Bytes per file.
 * @property {string[]} types     Allowed content types; empty allows any.
 */

/**
 * What a pending upload gives back: name it in the field of a create, `put` or `patch`.
 * @typedef {object} PreparedUpload
 * @property {string} id
 * @property {string} token
 * @property {{ upload: string, token: string }} ref   `{ upload, token }`: what goes in the document's field.
 * @property {string} expiresAt   RFC3339 timestamp: unused, it expires then.
 * @property {{ name: string, size: number, type: string, sha256: string }} file   What backd stored.
 */

/**
 * Options of an upload: `name` and `type` default to the file's own, `onProgress` reports
 * the bytes sent, `ifMatch` conditions a write to a document.
 * @typedef {PutOptions} UploadOptions
 */

/** @type {WeakMap<Client, Map<string, Promise<FileField>>>} */
const fields = new WeakMap()

/**
 * A field's configuration, asked once per client and remembered.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} field
 * @param {RequestOptions} [opts]
 * @returns {Promise<FileField>}
 */
export function fileField(client, collectionPath, field, opts) {
  let byKey = fields.get(client)
  if (!byKey) fields.set(client, (byKey = new Map()))
  const key = [...collectionPath, field].join('/')
  let found = byKey.get(key)
  if (!found) {
    found = client.request({ method: 'GET', path: [...collectionPath, '_files', field], ...opts }).then((r) => r.data)
    found.catch(() => byKey?.delete(key)) // a failure isn't remembered
    byKey.set(key, found)
  }
  return found
}

/**
 * @param {Blob | File | Uint8Array | ArrayBuffer | string} file
 * @returns {Blob}
 */
function asBlob(file) {
  if (typeof Blob !== 'undefined' && file instanceof Blob) return file
  return new Blob([/** @type {BlobPart} */ (file)])
}

/**
 * @param {Blob} blob
 * @param {UploadOptions} opts
 */
function describe(blob, opts) {
  const own = /** @type {{ name?: string }} */ (blob).name
  return { name: opts.name ?? (own || 'file'), type: opts.type ?? (blob.type || 'application/octet-stream'), size: blob.size }
}

/**
 * Sends a file to the signed link of a direct upload, the way a browser does: the
 * headers the link names, and no credentials of backd's.
 * @param {Client} client
 * @param {{ url: string, headers: Record<string, string> }} start
 * @param {Blob} blob
 * @param {UploadOptions} opts
 */
async function putToBucket(client, start, blob, opts) {
  let res
  try {
    res = await client.send(new URL(start.url), opts.onProgress, { method: 'PUT', headers: start.headers, body: blob, signal: opts.signal })
  } catch (cause) {
    throw new NetworkError({ status: 0, code: opts.signal?.aborted ? 'aborted' : 'network_error', message: opts.signal?.aborted ? 'request aborted' : 'network error: ' + String(cause), cause })
  }
  if (!res.ok) {
    throw new BackdError({ status: res.status, code: 'upload_failed', message: `the storage refused the file (${res.status}): is the bucket's CORS set for this origin, and was the file changed after it was declared?` })
  }
}

/**
 * Declares a file, sends it to the bucket and completes: the three steps of a direct
 * upload. `startPath` is where to start it; the completion is always under the collection.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string[]} startPath
 * @param {string} field
 * @param {Blob} blob
 * @param {UploadOptions} opts
 * @returns {Promise<{ start: any, answer: any }>}
 */
async function direct(client, collectionPath, startPath, field, blob, opts) {
  const { name, type, size } = describe(blob, opts)
  const sha256 = await sha256Blob(blob, { signal: opts.signal })
  const headers = opts.ifMatch === undefined ? opts.headers : { ...opts.headers, 'If-Match': typeof opts.ifMatch === 'number' ? `"${opts.ifMatch}"` : opts.ifMatch }
  const { data: start } = await client.request({ method: 'POST', path: startPath, body: { name, size, type, sha256 }, signal: opts.signal, headers })
  await putToBucket(client, start, blob, opts)
  const { data: answer } = await client.request({
    method: 'POST',
    path: [...collectionPath, '_files', field, 'uploads', start.upload_id, 'complete'],
    body: { upload_token: start.upload_token },
    signal: opts.signal,
    headers,
  })
  return { start, answer }
}

/**
 * Uploads a file into a document's file field, whichever way the field takes it: streamed
 * through backd (`upload: proxy`), or straight to the bucket with a signed link that backd
 * verifies afterwards (`upload: direct`, with the SHA-256 computed as the file is read).
 * Resolves with the updated document.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {Blob | File | Uint8Array | ArrayBuffer | string} file
 * @param {UploadOptions} [opts]
 * @returns {Promise<import('./data.js').Doc>}
 */
export async function uploadFile(client, collectionPath, id, field, file, opts = {}) {
  const blob = asBlob(file)
  const info = await fileField(client, collectionPath, field, { signal: opts.signal })
  if (info.upload === 'direct') {
    return (await direct(client, collectionPath, [...collectionPath, id, '_files', field, 'uploads'], field, blob, opts)).answer
  }
  const { name, type } = describe(blob, opts)
  return new Files(client, collectionPath, id, field).put(blob, { ...opts, name, type })
}

/**
 * Uploads a file for a document that doesn't exist yet (a pending upload), whichever way the
 * field takes it, and answers what to name in the field of the create: `{ upload: id, token }`.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} field
 * @param {Blob | File | Uint8Array | ArrayBuffer | string} file
 * @param {UploadOptions} [opts]
 * @returns {Promise<PreparedUpload>}
 */
export async function prepareUpload(client, collectionPath, field, file, opts = {}) {
  const blob = asBlob(file)
  const info = await fileField(client, collectionPath, field, { signal: opts.signal })
  const { name, type } = describe(blob, opts)
  /** @type {any} */
  let start
  /** @type {any} */
  let file2
  if (info.upload === 'direct') {
    const done = await direct(client, collectionPath, [...collectionPath, '_files', field, 'uploads'], field, blob, { ...opts, ifMatch: undefined })
    start = done.start
    file2 = done.answer.file
  } else {
    const { data } = await client.request({
      method: 'POST',
      path: [...collectionPath, '_files', field, 'uploads'],
      query: { name },
      rawBody: blob,
      contentType: type,
      onUploadProgress: opts.onProgress,
      signal: opts.signal,
      headers: opts.headers,
    })
    start = data
    file2 = data.file
  }
  return {
    id: start.upload_id,
    token: start.upload_token,
    ref: { upload: start.upload_id, token: start.upload_token },
    expiresAt: start.expires_at,
    file: file2,
  }
}

/**
 * A link to a file, for an app to show or open: `{ url, expiresAt }`.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {string} fileId
 * @param {RequestOptions} [opts]
 * @returns {Promise<{ url: string, expiresAt: string }>}
 */
export async function fileUrl(client, collectionPath, id, field, fileId, opts) {
  const l = await new Files(client, collectionPath, id, field).link(fileId, opts)
  return { url: l.url, expiresAt: l.expires_at }
}

/**
 * Starts a download in the browser: gets a fresh link and opens it. The link makes the
 * browser save the file and stay on the page. Browsers only.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {string} fileId
 * @param {RequestOptions} [opts]
 * @returns {Promise<{ url: string, expiresAt: string }>}
 */
export async function downloadFile(client, collectionPath, id, field, fileId, opts) {
  if (typeof window === 'undefined' || typeof window.location?.assign !== 'function') {
    throw new Error('downloadFile() only works in a browser: use fileUrl() to get a link, or files(id, field).get() to read the bytes')
  }
  const l = await fileUrl(client, collectionPath, id, field, fileId, opts)
  window.location.assign(l.url)
  return l
}
