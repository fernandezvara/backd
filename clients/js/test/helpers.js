/**
 * A fake fetch that answers from a list of expected calls, in order, and
 * records what was sent.
 * @param {Array<{ status?: number, body?: unknown, headers?: Record<string, string> } | Error>} answers
 */
export function mockFetch(answers) {
  /** @type {Array<{ url: URL, method: string, headers: Record<string, string>, body: any }>} */
  const calls = []
  const queue = [...answers]
  /**
   * @param {string | URL | Request} input
   * @param {RequestInit} [init]
   * @returns {Promise<Response>}
   */
  const fakeFetch = async (input, init = {}) => {
    const url = new URL(String(input))
    calls.push({
      url,
      method: init.method ?? 'GET',
      headers: /** @type {Record<string, string>} */ (init.headers ?? {}),
      body: typeof init.body === 'string' ? JSON.parse(init.body) : undefined,
    })
    const next = queue.shift()
    if (!next) throw new Error('unexpected request: ' + (init.method ?? 'GET') + ' ' + url.pathname)
    if (next instanceof Error) throw next
    const status = next.status ?? 200
    const body = next.body === undefined || status === 204 ? null : JSON.stringify(next.body)
    return new Response(body, { status, headers: { 'Content-Type': 'application/json', ...next.headers } })
  }
  return { fetch: fakeFetch, calls, remaining: () => queue.length }
}

/**
 * @param {string} code
 * @param {string} [message]
 */
export function errorBody(code, message = code) {
  return { error: { code, message, request_id: 'req-1' } }
}

export const user = {
  id: 'u1',
  email: 'ada@example.com',
  email_verified: false,
  roles: [],
  created_at: '2026-09-26T12:00:00.000Z',
}

/** @param {string} token */
export function session(token) {
  return { token, token_type: 'Bearer', session_id: 's1', expires_at: '2026-10-26T12:00:00.000Z', user }
}
