import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { expandAreas, NotAdminError, useSession } from './session'

interface Call {
  method: string
  path: string
  auth: string | null
}

let calls: Call[]
let routes: Record<string, () => Response>

const json = (status: number, body?: unknown) => new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const access = { level: 'read', write: [], read: ['config', 'functions'], read_access: { users: false, data: false }, user: { id: 'u1', email: 'ada@example.com', roles: ['dev'] } }

beforeEach(() => {
  calls = []
  sessionStorage.clear()
  routes = {
    'POST /v1/acme/_auth/login': () => json(200, { token: 'bds_tok', expires_at: '2030-01-01T00:00:00Z', user: { id: 'u1', email: 'ada@example.com' } }),
    'GET /v1/acme/_admin/whoami': () => json(200, access),
    'POST /v1/acme/_auth/logout': () => json(204),
  }
  vi.stubGlobal('fetch', async (input: Request | string | URL, init?: RequestInit) => {
    const req = new Request(input as string, init)
    const url = new URL(req.url)
    calls.push({ method: req.method, path: url.pathname, auth: req.headers.get('Authorization') })
    const route = routes[`${req.method} ${url.pathname}`]
    return route ? route() : json(404, { error: { code: 'not_found', message: 'no' } })
  })
  setActivePinia(createPinia())
})
afterEach(() => vi.unstubAllGlobals())

test('signing in keeps the token in memory and shows what whoami says', async () => {
  const s = useSession()
  await s.signIn('acme', 'ada@example.com', 'dev-p4ssw0rd!', false)
  expect(s.signedIn).toBe(true)
  expect(s.realm).toBe('acme')
  expect(s.email).toBe('ada@example.com')
  expect(s.canRead('config')).toBe(true)
  expect(s.canRead('users')).toBe(false)
  expect(s.canWrite('config')).toBe(false)
  expect(calls.find((c) => c.path.endsWith('/whoami'))?.auth).toBe('Bearer bds_tok')
  expect(sessionStorage.length).toBe(0)
  expect(localStorage.length).toBe(0)
  await s.signOut()
})

test('opting in stores the token in this tab only', async () => {
  const s = useSession()
  await s.signIn('acme', 'ada@example.com', 'dev-p4ssw0rd!', true)
  expect(sessionStorage.getItem('backd-admin:acme')).toBe('bds_tok')
  expect(localStorage.length).toBe(0)

  // A reload in the same tab: a fresh store restores from the tab's storage.
  setActivePinia(createPinia())
  const again = useSession()
  expect(await again.restore('acme')).toBe(true)
  expect(again.signedIn).toBe(true)
  await again.signOut()
  expect(sessionStorage.getItem('backd-admin:acme')).toBeNull()
})

test('sign-out revokes the session on the server', async () => {
  const s = useSession()
  await s.signIn('acme', 'ada@example.com', 'dev-p4ssw0rd!', false)
  await s.signOut()
  expect(s.signedIn).toBe(false)
  expect(s.ended).toBe('signed-out')
  const out = calls.find((c) => c.path === '/v1/acme/_auth/logout')
  expect(out?.method).toBe('POST')
  expect(out?.auth).toBe('Bearer bds_tok')
})

test('the idle timeout signs out and revokes', async () => {
  vi.useFakeTimers()
  try {
    const s = useSession()
    s.setIdleSeconds(30)
    await s.signIn('acme', 'ada@example.com', 'dev-p4ssw0rd!', false)
    await vi.advanceTimersByTimeAsync(31_000)
    expect(s.signedIn).toBe(false)
    expect(s.ended).toBe('idle')
    expect(calls.some((c) => c.path === '/v1/acme/_auth/logout')).toBe(true)
  } finally {
    vi.useRealTimers()
  }
})

test('a user with no admin role is not signed in, and their session is ended', async () => {
  routes['GET /v1/acme/_admin/whoami'] = () => json(403, { error: { code: 'forbidden', message: 'no admin role' } })
  const s = useSession()
  await expect(s.signIn('acme', 'bob@example.com', 'dev-p4ssw0rd!', false)).rejects.toBeInstanceOf(NotAdminError)
  expect(s.signedIn).toBe(false)
  expect(calls.some((c) => c.path === '/v1/acme/_auth/logout')).toBe(true)
})

test('"all" stands for every area', () => {
  expect(expandAreas(['all'])).toHaveLength(8)
  expect(expandAreas(['audit', 'users'])).toEqual(['users', 'audit'])
  expect(expandAreas([])).toEqual([])
})
