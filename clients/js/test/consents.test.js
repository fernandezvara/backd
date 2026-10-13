import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, ConsentRequiredError, SecondStepRequiredError } from '../src/index.js'
import { mockFetch, errorBody, session } from './helpers.js'

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const make = (m, extra = {}) => createClient({ url: 'http://api.test', realm: 'shelf', fetch: m.fetch, ...extra })

test('the texts are read without a session', async () => {
  const m = mockFetch([
    { body: { items: [{ name: 'terms', required: true, current: { version: '2026-10', reaccept: true }, locales: ['en'] }] } },
    { body: { name: 'terms', version: '2026-10', locale: 'es', text: 'Texto', hash: 'a'.repeat(64), current: true } },
  ])
  const c = make(m)
  const list = await c.consents.current()
  assert.equal(list[0].name, 'terms')
  const text = await c.consents.text('terms', '2026-10', 'es-MX')
  assert.equal(text.text, 'Texto')
  assert.equal(m.calls[0].url.pathname, '/v1/shelf/_consents')
  assert.equal(m.calls[1].url.pathname, '/v1/shelf/_consents/terms/2026-10')
  assert.equal(m.calls[1].url.searchParams.get('locale'), 'es-MX')
  assert.equal(m.calls[0].headers.Authorization, undefined)
})

test('accept, state and withdraw act for the signed-in user', async () => {
  const state = { consents: { terms: '2026-10' }, to_accept: [] }
  const m = mockFetch([{ body: state }, { body: { ...state, history: [] } }, { body: { consents: {}, to_accept: [] } }])
  const c = make(m, { apiKey: 'bdk_test' })
  assert.deepEqual(await c.consents.accept({ terms: '2026-10' }), state)
  assert.deepEqual(m.calls[0].body, { consents: { terms: '2026-10' } })
  assert.equal(m.calls[0].method, 'POST')
  assert.equal((await c.consents.state()).consents.terms, '2026-10')
  assert.equal(m.calls[1].url.pathname, '/v1/shelf/_auth/consents')
  await c.consents.withdraw('marketing')
  assert.equal(m.calls[2].method, 'DELETE')
  assert.equal(m.calls[2].url.pathname, '/v1/shelf/_auth/consents/marketing')
})

test('signup carries the consents, and a refusal is typed', async () => {
  const m = mockFetch([
    { status: 400, body: { error: { code: 'consent_required', message: 'required consents must be accepted', details: [{ path: 'consents.terms', reason: 'must be accepted at version 2026-10' }], request_id: 'r' } } },
    { status: 201, body: session('bds_ok') },
  ])
  const c = make(m)
  const input = { email: 'ada@example.com', password: 'dev-p4ssw0rd!' }
  await assert.rejects(c.auth.signup(input), (err) => {
    assert.ok(err instanceof ConsentRequiredError)
    assert.deepEqual(err.consents, [{ name: 'terms', version: '2026-10' }])
    return true
  })
  await c.auth.signup({ ...input, consents: { terms: '2026-10' } })
  assert.deepEqual(m.calls[1].body.consents, { terms: '2026-10' })
})

test('a sign-in that needs consents is a challenge, finished with acceptConsents', async () => {
  const m = mockFetch([
    { body: { consent_required: true, challenge: 'ch-1', consents: [{ name: 'terms', version: '2026-10' }] } },
    { body: { consent_required: true, challenge: 'ch-2', consents: [{ name: 'terms', version: '2026-10' }] } },
    { body: session('bds_tok') },
  ])
  const c = make(m)
  const step = await c.auth.signIn({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal('consentRequired' in step && step.consentRequired, true)
  assert.deepEqual('consents' in step && step.consents, [{ name: 'terms', version: '2026-10' }])
  // login() promises a session: the step becomes an error that carries the challenge.
  await assert.rejects(c.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' }), (err) => {
    assert.ok(err instanceof SecondStepRequiredError)
    assert.equal(err.code, 'consent_required')
    assert.equal(err.challenge, 'ch-2')
    assert.deepEqual(err.consents, [{ name: 'terms', version: '2026-10' }])
    return true
  })
  const s = await c.auth.acceptConsents('ch-2', { terms: '2026-10' })
  assert.equal(s.token, 'bds_tok')
  assert.deepEqual(m.calls[2].body, { challenge: 'ch-2', consents: { terms: '2026-10' } })
  assert.equal(m.calls[2].url.pathname, '/v1/shelf/_auth/consents/accept')
  assert.equal(await c.auth.hasSession(), true)
})

test('a request refused after a reaccept_by date is a ConsentRequiredError', async () => {
  const m = mockFetch([
    { status: 403, body: { error: { code: 'consent_required', message: 'consents must be accepted', details: [{ path: 'consents.terms', reason: 'must be accepted at version 3' }], request_id: 'r' } } },
    { body: errorBody('x') },
  ])
  const c = make(m, { apiKey: 'bdk_test' })
  await assert.rejects(c.db('main').collection('posts').list(), (err) => {
    assert.ok(err instanceof ConsentRequiredError)
    assert.equal(err.status, 403)
    assert.deepEqual(err.consents, [{ name: 'terms', version: '3' }])
    return true
  })
})

test('magic links and providers carry the consents of a new account', async () => {
  const m = mockFetch([{ status: 202, body: { request_id: 'req-1' } }, { body: { authorize_url: 'https://idp.example/auth' } }])
  const c = make(m)
  const { newPKCE } = await import('../src/oauth.js')
  assert.ok(newPKCE)
  await c.auth.magicLink.request('new@example.com', { redirectTo: 'https://app.example/in', consents: { terms: '2026-10' } })
  assert.deepEqual(m.calls[0].body.consents, { terms: '2026-10' })
  await c.auth.signInWith('google', { redirectTo: 'https://app.example/in', consents: { terms: '2026-10' }, navigate: () => {} })
  assert.deepEqual(m.calls[1].body.consents, { terms: '2026-10' })
})

test('confirming an enrolment keeps the recovery codes when consents follow', async () => {
  const m = mockFetch([
    { body: { recovery_codes: ['aaaaa-bbbbb'], consent_required: true, challenge: 'ch-9', consents: [{ name: 'terms', version: '2026-10' }] } },
  ])
  const c = make(m)
  const done = await c.auth.mfa.confirm({ setupId: 's1', code: '123456', challenge: 'ch-1' })
  assert.deepEqual(done.recovery_codes, ['aaaaa-bbbbb'])
  assert.equal(done.session, undefined)
  assert.deepEqual(done.consent, { consentRequired: true, challenge: 'ch-9', consents: [{ name: 'terms', version: '2026-10' }] })
})
