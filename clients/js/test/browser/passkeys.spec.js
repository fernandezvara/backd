import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'

// Passkeys in a real browser: Chromium's virtual authenticator (a CTAP2 device with a resident key and
// user verification) makes and signs for the JS client against a real backd. The page is served from
// http://localhost:18081, the origin the `passkeys` realm of the integration stack is bound to, and the
// API is reached through the same origin (the server below proxies /v1/ to backd).

const BACKD = process.env.BACKD_URL ?? ''
const PORT = 18081
const src = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'src')
const password = 'dev-p4ssw0rd!'

/** @type {import('node:http').Server} */
let server

test.skip(BACKD === '', 'BACKD_URL not set; run `make js-integration`')

test.beforeAll(async () => {
  server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? '/', 'http://localhost')
      if (url.pathname === '/') {
        res.setHeader('Content-Type', 'text/html')
        res.end('<!doctype html><title>passkeys</title><input id="email" autocomplete="username webauthn">')
      } else if (url.pathname.startsWith('/src/') && /^[\w./-]+$/.test(url.pathname) && !url.pathname.includes('..')) {
        res.setHeader('Content-Type', 'text/javascript')
        res.end(await readFile(join(src, url.pathname.slice(5))))
      } else if (url.pathname.startsWith('/v1/')) {
        const chunks = []
        for await (const c of req) chunks.push(c)
        const headers = /** @type {any} */ ({ ...req.headers })
        delete headers.host
        const upstream = await fetch(BACKD + url.pathname + url.search, { method: req.method, headers, body: chunks.length ? Buffer.concat(chunks) : undefined })
        res.statusCode = upstream.status
        upstream.headers.forEach((v, k) => {
          if (!['content-encoding', 'content-length', 'transfer-encoding'].includes(k)) res.setHeader(k, v)
        })
        res.end(Buffer.from(await upstream.arrayBuffer()))
      } else {
        res.statusCode = 404
        res.end()
      }
    } catch (e) {
      res.statusCode = 500
      res.end(String(e))
    }
  })
  await new Promise((resolve) => server.listen(PORT, '127.0.0.1', () => resolve(undefined)))
})

test.afterAll(async () => {
  await new Promise((resolve) => server?.close(() => resolve(undefined)))
})

test('register, sign in, use it as a later step, re-authenticate and remove', async ({ page, context }) => {
  await page.goto(`http://localhost:${PORT}/`)
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  })
  await page.evaluate(async () => {
    // @ts-ignore (served by the page's own server)
    const { createClient } = await import('/src/index.js')
    // @ts-ignore
    window.backd = createClient({ url: location.origin, realm: 'passkeys' })
  })
  const email = `pk.${Date.now().toString(36)}@example.com`

  // The browser can do it, with conditional UI.
  expect(await page.evaluate(() => /** @type {any} */ (window).backd.auth.passkeys.supported())).toEqual({ available: true, conditional: true })

  // Register: a password account adds a passkey after proving who it is.
  const registered = await page.evaluate(async ({ email, password }) => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.signup({ email, password, strategy: 'password' })
    await auth.reauth({ method: 'password', password })
    const made = await auth.passkeys.register({ name: 'Laptop' })
    return { made, list: await auth.passkeys.list() }
  }, { email, password })
  expect(registered.made.name).toBe('Laptop')
  expect(registered.list).toHaveLength(1)
  expect(registered.list[0].id).toBe(registered.made.id)
  // The key is in the authenticator, resident and bound to localhost.
  const held = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
  expect(held.credentials).toHaveLength(1)
  expect(held.credentials[0].rpId).toBe('localhost')
  expect(held.credentials[0].isResidentCredential).toBe(true)

  // Sign in with it alone: no email, no password.
  const signedIn = await page.evaluate(async () => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.logout()
    const s = await auth.passkeys.signIn({ strategy: 'passkey' })
    return { email: s.user.email, strategy: s.strategy, token: Boolean(await auth.token()) }
  })
  expect(signedIn).toEqual({ email, strategy: 'passkey', token: true })

  // Conditional UI: with a passkey in the authenticator the autofill request resolves at once; a signal that
  // aborts stops the wait (and the call ends with the AbortError).
  const conditional = await page.evaluate(async () => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.logout()
    const s = await auth.passkeys.signIn({ strategy: 'passkey', conditional: true, signal: new AbortController().signal })
    await auth.logout()
    const ac = new AbortController()
    ac.abort()
    const aborted = await auth.passkeys.signIn({ strategy: 'passkey', conditional: true, signal: ac.signal }).then(() => 'signed in', (/** @type {any} */ e) => e.name)
    return { email: s.user.email, aborted }
  })
  expect(conditional).toEqual({ email, aborted: 'AbortError' })

  // As a later step of a strategy: the password, then the passkey.
  const later = await page.evaluate(async ({ email, password }) => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.logout()
    const step = await auth.signIn({ email, password, strategy: 'password_passkey' })
    const before = { challenge: 'challenge' in step, next: step.next, token: await auth.token() }
    const s = await auth.verifyStep(step.challenge, 'passkey')
    return { before, strategy: s.strategy, token: Boolean(await auth.token()) }
  }, { email, password })
  expect(later).toEqual({ before: { challenge: true, next: ['passkey'], token: null }, strategy: 'password_passkey', token: true })

  // Re-authenticating with it, and what needs that.
  const proof = await page.evaluate(async () => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.reauth({ method: 'passkey' })
    return auth.hasFreshProof()
  })
  expect(proof).toBe(true)

  // Rename and remove it: the session it opened ends with it, and the password still works.
  const removed = await page.evaluate(async ({ email, password }) => {
    const { auth } = /** @type {any} */ (window).backd
    const [p] = await auth.passkeys.list()
    const renamed = await auth.passkeys.rename(p.id, 'Work laptop')
    await auth.passkeys.remove(p.id)
    const ended = await auth.passkeys.list().then(() => 'still open', (/** @type {any} */ e) => e.name)
    await auth.signIn({ email, password, strategy: 'password' })
    return { name: renamed.name, ended, left: (await auth.passkeys.list()).length }
  }, { email, password })
  expect(removed).toEqual({ name: 'Work laptop', ended: 'AuthenticationError', left: 0 })

  // A removed passkey signs nobody in: the browser still holds the key, the server no longer knows it.
  const refused = await page.evaluate(async () => {
    const { auth } = /** @type {any} */ (window).backd
    await auth.logout()
    return auth.passkeys.signIn({ strategy: 'passkey' }).then(() => 'signed in', (/** @type {any} */ e) => e.code)
  })
  expect(refused).toBe('invalid_passkey')
})
