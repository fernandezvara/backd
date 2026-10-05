// What the server tells the page (GET /_ui/config.json): today only the
// idle timeout (BACKD_ADMIN_UI_IDLE).

export interface RuntimeConfig {
  idleSeconds: number
}

export const DEFAULT_IDLE_SECONDS = 30 * 60

export async function loadRuntimeConfig(fetchImpl: typeof fetch = fetch): Promise<RuntimeConfig> {
  try {
    const res = await fetchImpl(`${import.meta.env.BASE_URL}config.json`, { cache: 'no-store' })
    if (res.ok) {
      const body = (await res.json()) as { idle_seconds?: unknown }
      if (typeof body.idle_seconds === 'number' && body.idle_seconds > 0) return { idleSeconds: body.idle_seconds }
    }
  } catch {
    // keep the default: a missing config must not stop the page
  }
  return { idleSeconds: DEFAULT_IDLE_SECONDS }
}
