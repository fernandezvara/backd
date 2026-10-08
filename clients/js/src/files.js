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
 * @property {boolean} [custom]       Made with parameters a function gave (`generate`), not the declared ones.
 * @property {string} [generated_at]  RFC3339 timestamp.
 */

/**
 * Options of `get` and `link`: `version` names a made version of an image (`versions:` in
 * collection.yaml) to read instead of the original.
 * @typedef {RequestOptions & { version?: string }} VersionOptions
 */

/**
 * Parameters to generate a version with, named as in `collection.yaml`: a box (`max_width`,
 * `max_height`, at least one), and optionally `fit` (`contain`, `cover`, `stretch`), `quality`
 * (1 to 100, jpeg), `format` (`jpeg`, `png`) and `upscale`.
 * @typedef {object} VersionParams
 * @property {number} [max_width]
 * @property {number} [max_height]
 * @property {'contain' | 'cover' | 'stretch'} [fit]
 * @property {number} [quality]
 * @property {'jpeg' | 'png'} [format]
 * @property {boolean} [upscale]
 */

/**
 * A made version of one file (`files(id, field).version(name)`): make it again, make it
 * with other parameters, or drop it. Each waits for a worker to do it and resolves with the
 * version's state.
 * @typedef {object} FileVersion
 * @property {(opts?: RequestOptions) => Promise<VersionState>} regenerate   Again, with the parameters `collection.yaml` declares.
 * @property {(params: VersionParams, opts?: RequestOptions) => Promise<VersionState>} generate   With other parameters: only a `writable` version allows it (`version_not_writable`).
 * @property {(opts?: RequestOptions) => Promise<VersionState>} delete   Drops the copy: the version is `pending` again (a worker makes it) or `empty` (only functions make it).
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
   * @param {VersionOptions} [opts]  `version` reads a made version of an image instead: a `BackdError` with code `version_unavailable` when it isn't ready.
   * @returns {Promise<FileContent>}
   */
  async get(fileId, opts) {
    const { version, ...rest } = opts ?? {}
    const found = await this.#which(fileId, rest)
    const { response } = await this.client.request({ method: 'GET', path: [...this.path, found.fileId], query: { version }, raw: true, ...rest })
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
   * One of the file's declared versions (`versions:` in collection.yaml), to make again,
   * generate with other parameters, or delete. They are requests that wait for a worker, so a
   * function can use them within its time limit; the caller's `update` rule applies. Errors
   * carry the reason as their `code`: `not_an_image`, `unsupported_format`, `too_large`,
   * `decode_error`, `timeout` (422), `version_not_writable` (403), `version_timeout` (504, no
   * worker).
   * @param {string} name       The version's name.
   * @param {string} [fileId]   Which file; without it the one a single field holds.
   * @returns {FileVersion}
   */
  version(name, fileId) {
    /** @param {RequestOptions | undefined} opts */
    const path = async (opts) => [...this.path, (await this.#which(fileId, opts)).fileId, 'versions', name]
    return {
      regenerate: async (opts) => (await this.client.request({ method: 'POST', path: await path(opts), ...opts })).data,
      generate: async (params, opts) => (await this.client.request({ method: 'POST', path: await path(opts), body: params, ...opts })).data,
      delete: async (opts) => (await this.client.request({ method: 'DELETE', path: await path(opts), ...opts })).data,
    }
  }

  /**
   * A link to download the file without credentials, until it expires: for an app to open or
   * show (`{ url, expires_at }`). Without `fileId` the one a single field holds.
   * @param {string} [fileId]
   * @param {VersionOptions} [opts]  `version` links a made version of an image instead: a `BackdError` with code `version_unavailable` when it isn't ready.
   * @returns {Promise<FileLink>}
   */
  async link(fileId, opts) {
    const { version, ...rest } = opts ?? {}
    const found = await this.#which(fileId, rest)
    return (await this.client.request({ method: 'GET', path: [...this.path, found.fileId], query: { link: 'json', version }, ...rest })).data
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
 * @property {number} [multipart_above]   A direct field that can hold more: files above this many bytes are sent in parts.
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

const MIB = 1024 * 1024
const MAX_PARTS = 1000
const PART_ATTEMPTS = 4
const PART_CONCURRENCY = 3

/**
 * The size of the parts of a file sent in parts: at least 64 MiB, and a multiple of 16 MiB
 * when the file needs more to stay within 1000 parts. backd checks it, so it is the same on
 * both sides.
 * @param {number} size
 */
export function partSizeFor(size) {
  const need = Math.ceil(Math.ceil(size / MAX_PARTS) / (16 * MIB)) * 16 * MIB
  return Math.max(64 * MIB, need)
}

/** @param {number} ms @param {AbortSignal} [signal] */
const pause = (ms, signal) =>
  new Promise((resolve, reject) => {
    const t = setTimeout(resolve, ms)
    signal?.addEventListener('abort', () => (clearTimeout(t), reject(signal.reason)), { once: true })
  })

/**
 * Sends a file in parts, each to its own signed link: a few at a time, a part that fails to
 * go through is sent again (the others are not). Progress is that of the whole file.
 * @param {Client} client
 * @param {{ part_size: number, parts: { number: number, url: string, headers: Record<string, string>, size: number }[] }} start
 * @param {Blob} blob
 * @param {UploadOptions} opts
 */
async function putParts(client, start, blob, opts) {
  const sent = new Array(start.parts.length).fill(0)
  const report = opts.onProgress
    ? () => opts.onProgress?.({ loaded: sent.reduce((a, b) => a + b, 0), total: blob.size })
    : undefined
  let next = 0
  async function worker() {
    while (next < start.parts.length) {
      const i = next++
      const part = start.parts[i]
      const body = blob.slice(i * start.part_size, i * start.part_size + part.size)
      for (let attempt = 1; ; attempt++) {
        try {
          await putToBucket(client, part, body, {
            ...opts,
            onProgress: report && ((p) => ((sent[i] = p.loaded), report())),
          })
          break
        } catch (e) {
          const retryable = e instanceof NetworkError || (e instanceof BackdError && e.status >= 500)
          if (!retryable || attempt >= PART_ATTEMPTS || opts.signal?.aborted) throw e
          sent[i] = 0
          await pause(500 * 2 ** (attempt - 1), opts.signal)
        }
      }
      sent[i] = part.size
      report?.()
    }
  }
  await Promise.all(Array.from({ length: Math.min(PART_CONCURRENCY, start.parts.length) }, worker))
}

/**
 * Declares a file, sends it to the bucket and completes: the three steps of a direct
 * upload. `startPath` is where to start it; the completion is always under the collection.
 * A file above the field's `multipart_above` is declared by the SHA-256 of each part and
 * sent in parts.
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string[]} startPath
 * @param {FileField} info
 * @param {Blob} blob
 * @param {UploadOptions} opts
 * @returns {Promise<{ start: any, answer: any }>}
 */
async function direct(client, collectionPath, startPath, info, blob, opts) {
  const { name, type, size } = describe(blob, opts)
  const field = info.name
  const multipart = info.multipart_above !== undefined && size > info.multipart_above
  const declared = multipart ? { part_sha256: await partDigests(blob, partSizeFor(size), opts.signal) } : { sha256: await sha256Blob(blob, { signal: opts.signal }) }
  const headers = opts.ifMatch === undefined ? opts.headers : { ...opts.headers, 'If-Match': typeof opts.ifMatch === 'number' ? `"${opts.ifMatch}"` : opts.ifMatch }
  const { data: start } = await client.request({ method: 'POST', path: startPath, body: { name, size, type, ...declared }, signal: opts.signal, headers })
  if (start.multipart) await putParts(client, start, blob, opts)
  else await putToBucket(client, start, blob, opts)
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
 * @param {Blob} blob
 * @param {number} partSize
 * @param {AbortSignal} [signal]
 */
async function partDigests(blob, partSize, signal) {
  /** @type {string[]} */
  const out = []
  for (let at = 0; at < blob.size; at += partSize) out.push(await sha256Blob(blob.slice(at, at + partSize), { signal }))
  return out
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
    return (await direct(client, collectionPath, [...collectionPath, id, '_files', field, 'uploads'], info, blob, opts)).answer
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
    const done = await direct(client, collectionPath, [...collectionPath, '_files', field, 'uploads'], info, blob, { ...opts, ifMatch: undefined })
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
 * A link to a file, for an app to show or open: `{ url, expiresAt }`. With `version` it is a
 * link to that made version of an image, or `null` while the version isn't ready (pending,
 * skipped, failed, or not made by a worker yet): show a placeholder, and read the document
 * (`versionStatus`) to tell why.
 * @overload
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {string} fileId
 * @param {RequestOptions & { version?: undefined }} [opts]
 * @returns {Promise<{ url: string, expiresAt: string }>}
 */
/**
 * @overload
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {string} fileId
 * @param {RequestOptions & { version: string }} opts
 * @returns {Promise<{ url: string, expiresAt: string } | null>}
 */
/**
 * @param {Client} client
 * @param {string[]} collectionPath
 * @param {string} id
 * @param {string} field
 * @param {string} fileId
 * @param {VersionOptions} [opts]
 */
export async function fileUrl(client, collectionPath, id, field, fileId, opts) {
  try {
    const l = await new Files(client, collectionPath, id, field).link(fileId, opts)
    return { url: l.url, expiresAt: l.expires_at }
  } catch (e) {
    if (opts?.version !== undefined && e instanceof BackdError && e.code === 'version_unavailable') return null
    throw e
  }
}

/**
 * What a document says about a made version of one of its files, to show "processing…" or
 * "no preview" and to reserve room for the picture: `{ status, ready, reason?, width?,
 * height? }`. `status` is `pending`, `ready`, `empty`, `skipped` or `failed`, and
 * `undeclared` for a version the file's details don't name (the field declares none, or
 * it was declared after the file was uploaded).
 * @param {FileDetails} file    A file as a document holds it.
 * @param {string} name         The version's name.
 * @returns {{ status: VersionState['status'] | 'undeclared', ready: boolean, reason?: string, width?: number, height?: number }}
 */
export function versionStatus(file, name) {
  const v = file?.versions?.[name]
  if (!v) return { status: 'undeclared', ready: false }
  return { status: v.status, ready: v.status === 'ready', reason: v.reason, width: v.width, height: v.height }
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
