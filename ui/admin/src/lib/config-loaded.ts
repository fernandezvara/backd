// A dev-mode backd (BACKD_DEV) whose config hasn't loaded yet answers every API call with
// 503 config_not_loaded and shows why on its root page. The interface asks once at start,
// and again while it waits, so it can say so instead of offering a sign-in that can't work.

export const CONFIG_NOT_LOADED = 'config_not_loaded'

/** The server's message when its config hasn't loaded, or null when it has (or can't tell). */
export async function configNotLoaded(fetchImpl: typeof fetch = fetch, origin = window.location.origin): Promise<string | null> {
  try {
    const res = await fetchImpl(`${origin}/readyz`, { cache: 'no-store' })
    if (res.status !== 503) return null
    const body = (await res.json()) as { error?: { code?: unknown; message?: unknown } }
    if (body.error?.code !== CONFIG_NOT_LOADED) return null
    return typeof body.error.message === 'string' ? body.error.message : ''
  } catch {
    return null // unreachable, or not JSON: the usual errors say so where they happen
  }
}
