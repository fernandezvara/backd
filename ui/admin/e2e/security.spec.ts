import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

const base = process.env.E2E_URL ?? 'http://localhost:8080'

async function login(request: import('@playwright/test').APIRequestContext, who: keyof typeof users): Promise<Record<string, string>> {
  const res = await request.post(`/v1/${REALM}/_auth/login`, { data: { email: users[who], password: PASSWORD } })
  expect(res.ok()).toBe(true)
  return { Authorization: `Bearer ${(await res.json()).token}`, 'Content-Type': 'application/json' }
}

test('the page is served with a strict CSP and the other security headers', async ({ request }) => {
  for (const path of ['/_ui/', `/_ui/r/${REALM}/data`, '/_ui/nothing.png', '/_ui/config.json']) {
    const res = await request.get(path)
    const h = res.headers()
    const csp = h['content-security-policy'] ?? ''
    expect(csp, path).toContain("default-src 'none'")
    expect(csp, path).toContain("connect-src 'self'")
    expect(csp, path).toContain("frame-ancestors 'none'")
    expect(csp, path).toContain("require-trusted-types-for 'script'")
    expect(csp, path).not.toMatch(/unsafe-(inline|eval)/)
    expect(h['x-content-type-options'], path).toBe('nosniff')
    expect(h['x-frame-options'], path).toBe('DENY')
    expect(h['referrer-policy'], path).toBe('no-referrer')
  }
  expect((await request.get('/_ui/')).headers()['cache-control']).toBe('no-store')
})

test('the browser refuses markup sinks: Trusted Types are enforced', async ({ page, signIn }) => {
  await signIn('admin')
  const outcome = await page.evaluate(() => {
    try {
      document.body.innerHTML = '<img src=x>'
      return 'allowed'
    } catch (e) {
      return (e as Error).name
    }
  })
  expect(outcome).toBe('TypeError')
  // The attempt is a violation by design: it must not fail this test's own guard.
  await page.evaluate(() => (window.__violations = []))
})

test('a session held only in memory is revoked when the page is left', async ({ page, signIn, request }) => {
  const tokens: string[] = []
  page.on('request', (req) => {
    const auth = req.headers()['authorization']
    if (auth?.startsWith('Bearer ')) tokens.push(auth.slice(7))
  })
  await signIn('admin')
  await expect(page.getByTestId('level')).toBeVisible()
  const token = tokens.at(-1)!
  expect((await request.get(`/v1/${REALM}/_admin/whoami`, { headers: { Authorization: `Bearer ${token}` } })).status()).toBe(200)
  await page.reload()
  await expect.poll(async () => (await request.get(`/v1/${REALM}/_admin/whoami`, { headers: { Authorization: `Bearer ${token}` } })).status()).toBe(401)
})

test('the server enforces the levels, whatever the interface shows', async ({ request }) => {
  const doc = { name: 'refused' }
  const viewer = await login(request, 'viewer')
  // Read-only: reads (users and data are granted in this realm) but never changes.
  expect((await request.get(`/v1/${REALM}/_admin/data/main/products?limit=1`, { headers: viewer })).status()).toBe(200)
  expect((await request.get(`/v1/${REALM}/_admin/users?limit=1`, { headers: viewer })).status()).toBe(200)
  for (const [method, path, body] of [
    ['POST', `/v1/${REALM}/_admin/data/main/products`, doc],
    ['POST', `/v1/${REALM}/_admin/users`, { email: 'x@adminui.example' }],
    ['POST', `/v1/${REALM}/_admin/apikeys`, { name: 'nope' }],
    ['PUT', `/v1/${REALM}/_admin/secrets/NOPE`, { value: 'x' }],
    ['POST', `/v1/${REALM}/_admin/invitations`, {}],
    ['POST', `/v1/${REALM}/_admin/functions/main/greet/invoke`, { input: null }],
  ] as const) {
    const res = await request.fetch(path, { method, headers: viewer, data: body })
    expect(res.status(), `${method} ${path} as a read-only administrator`).toBe(403)
  }

  // A custom level reaches only its areas.
  const support = await login(request, 'support')
  for (const path of [`/_admin/data/main/products`, '/_admin/config', '/_admin/audit', '/_admin/apikeys', '/_admin/secrets', '/_admin/jobs']) {
    expect((await request.get(`/v1/${REALM}${path}`, { headers: support })).status(), `GET ${path} as support`).toBe(403)
  }
  expect((await request.get(`/v1/${REALM}/_admin/users?limit=1`, { headers: support })).status()).toBe(200)

  // A user with no admin role has no admin API at all.
  const plain = await login(request, 'plain')
  expect((await request.get(`/v1/${REALM}/_admin/whoami`, { headers: plain })).status()).toBe(403)
  expect((await request.get(`/v1/${REALM}/_admin/data/main/products`, { headers: plain })).status()).toBe(403)
})

test('writes through the data route are audited, never their content', async () => {
  const client = createClient({ url: base, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const secret = `audit-content-${Date.now().toString(36)}`
  const made = await client.admin.data('main', 'products').create({ name: secret, price: 1 })
  await client.admin.data('main', 'products').list({ limit: 1 }) // a read: not in the trail

  const page = await client.admin.audit.list({ action: 'data.create', target: `doc:main/products/${made.id}` })
  expect(page.items).toHaveLength(1)
  expect(page.items[0].actor).toMatch(/^user:/)
  expect(JSON.stringify(page.items[0])).not.toContain(secret)

  expect((await client.admin.audit.list({ action: 'data.read' })).items).toHaveLength(0)
  await client.auth.logout()
})
