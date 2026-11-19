import { useAdmin } from '@/lib/admin'

/**
 * A call to an admin route the JS client has no method for yet (app configuration, trigger health,
 * compliance, backup, second-factor coverage…). Same session, same errors as the client's own
 * methods; `path` is what follows `/_admin/`, one segment per item.
 */
export interface RawOptions {
  body?: unknown
  query?: Record<string, string | number | boolean | undefined>
  headers?: Record<string, string>
  /** The call needs a fresh proof of identity (the client sends it when it holds one). */
  reauth?: boolean
}
export type RawAdmin = <T = unknown>(method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string[], opts?: RawOptions) => Promise<T>

type Request = { method: string; path: string[] } & RawOptions

export function useRawAdmin(): RawAdmin {
  const admin = useAdmin() as unknown as { _request: (r: Request) => Promise<{ data: unknown }> }
  return async <T,>(method: string, path: string[], opts: RawOptions = {}) => (await admin._request({ method, path, ...opts })).data as T
}
