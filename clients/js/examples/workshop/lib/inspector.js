// The inspector: a fetch wrapper that records every call the app makes, so
// the page can show what the browser really sent and what backd answered:
// the request, the response, the request id, the function's own console
// output (backd sends it back only in dev mode, which the local stack runs)
// and a curl command that repeats the call.
//
// It never records the session token: curl shows $TOKEN instead.

const MAX_BODY = 6000

/**
 * Pretty-prints a JSON text; anything else comes back unchanged.
 * @param {string} text
 */
function pretty(text) {
  if (!text) return ''
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

/** @param {string} text */
function truncate(text) {
  return text.length > MAX_BODY ? `${text.slice(0, MAX_BODY)}\n… (${text.length - MAX_BODY} more characters)` : text
}

/**
 * A curl command for the call, with the token left out.
 * @param {{ method: string, url: string, headers: Record<string, string>, body: string }} call
 */
export function curlOf({ method, url, headers, body }) {
  const parts = [`curl -s -X ${method} '${url}'`]
  for (const [name, value] of Object.entries(headers)) {
    if (name.toLowerCase() === 'authorization') parts.push(`-H 'Authorization: Bearer $TOKEN'`)
    else parts.push(`-H '${name}: ${value}'`)
  }
  if (body) parts.push(`-d '${body.replaceAll("'", `'\\''`)}'`)
  return parts.join(' \\\n  ')
}

/**
 * One call the app made.
 * @typedef {object} Entry
 * @property {number} id
 * @property {string | null} step      The tour step that made it.
 * @property {string} method
 * @property {string} path             The URL's path without `/v1/<realm>`.
 * @property {string} url
 * @property {number | null} status    Null until it answers (or when it failed).
 * @property {number | null} ms
 * @property {string | null} requestId
 * @property {string} request          The request body, pretty-printed.
 * @property {string} response         The answer, pretty-printed.
 * @property {{ level: string, line: string }[]} logs   The function's console lines (dev mode only).
 * @property {string | null} executorMs
 * @property {string | null} error     Why the call failed, when it never got an answer.
 * @property {string} curl
 */

/**
 * @param {{ fetch?: typeof fetch, now?: () => number, onChange?: () => void }} [options]
 */
export function createInspector({ fetch: inner = globalThis.fetch.bind(globalThis), now = () => performance.now(), onChange = () => {} } = {}) {
  /** Newest first.
   * @type {Entry[]} */
  const entries = []
  /** @type {string | null} */
  let step = null
  let seq = 0

  /**
   * @param {string | URL | Request} input
   * @param {RequestInit} [init]
   */
  async function inspected(input, init = {}) {
    const url = String(input)
    const method = (init.method ?? 'GET').toUpperCase()
    /** @type {Record<string, string>} */
    const headers = {}
    for (const [k, v] of new Headers(init.headers ?? {})) headers[k] = v
    const body = typeof init.body === 'string' ? init.body : ''
    const started = now()
    /** @type {Entry} */
    const entry = {
      id: ++seq,
      step,
      method,
      path: new URL(url, globalThis.location?.href ?? 'http://localhost').pathname.replace(/^\/v1\/[^/]+/, ''),
      url,
      status: null,
      ms: null,
      requestId: null,
      request: truncate(pretty(body)),
      response: '',
      logs: [],
      executorMs: null,
      error: null,
      curl: curlOf({ method, url, headers, body }),
    }
    entries.unshift(entry)
    onChange()
    try {
      const res = await inner(input, init)
      entry.status = res.status
      entry.requestId = res.headers.get('X-Request-ID')
      const devLogs = res.headers.get('X-Backd-Dev-Logs')
      if (devLogs) {
        try {
          entry.logs = JSON.parse(devLogs)
        } catch {
          // Not what we expect: leave the logs out.
        }
      }
      entry.executorMs = res.headers.get('X-Backd-Dev-Duration-Ms')
      entry.response = truncate(pretty(await res.clone().text()))
      return res
    } catch (e) {
      entry.error = e instanceof Error ? e.message : String(e)
      throw e
    } finally {
      entry.ms = Math.round(now() - started)
      onChange()
    }
  }

  return {
    entries,
    fetch: inspected,
    /** Labels the calls that follow, until the next label. */
    /** @param {string | null} label */
    step(label) {
      step = label
    },
    clear() {
      entries.length = 0
      onChange()
    },
  }
}
