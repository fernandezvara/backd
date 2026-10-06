/**
 * @typedef {(p: { loaded: number, total: number }) => void} ProgressFn
 */

/**
 * A `fetch` made with XMLHttpRequest, because only it reports how much of an upload has
 * been sent. It answers a `Response` like fetch does. Browsers only.
 * @param {string | URL} url
 * @param {{ method: string, headers?: Record<string, string>, body?: any, signal?: AbortSignal, credentials?: 'include' }} init
 * @param {ProgressFn} [onProgress]
 * @returns {Promise<Response>}
 */
export function xhrFetch(url, init, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(init.method, String(url))
    xhr.responseType = 'arraybuffer'
    for (const [k, v] of Object.entries(init.headers ?? {})) xhr.setRequestHeader(k, v)
    xhr.withCredentials = init.credentials === 'include'
    if (onProgress) {
      xhr.upload.onprogress = (e) => onProgress({ loaded: e.loaded, total: e.lengthComputable ? e.total : 0 })
    }
    xhr.onload = () => {
      const headers = new Headers()
      for (const line of xhr.getAllResponseHeaders().trim().split(/[\r\n]+/)) {
        const i = line.indexOf(':')
        if (i > 0) headers.append(line.slice(0, i).trim(), line.slice(i + 1).trim())
      }
      const empty = [101, 204, 205, 304].includes(xhr.status)
      resolve(new Response(empty ? null : xhr.response, { status: xhr.status, headers }))
    }
    xhr.onerror = () => reject(new TypeError('network error'))
    xhr.ontimeout = () => reject(new TypeError('network timeout'))
    xhr.onabort = () => reject(init.signal?.reason ?? new DOMException('aborted', 'AbortError'))
    if (init.signal) {
      if (init.signal.aborted) return xhr.abort()
      init.signal.addEventListener('abort', () => xhr.abort(), { once: true })
    }
    xhr.send(init.body ?? null)
  })
}
