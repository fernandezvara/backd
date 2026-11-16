import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createClient, DomainTakenError, DomainExistsError, DomainsLimitError, ValidationError, NotFoundError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

const claim = { domain: 'acme.example', state: 'pending', discoverable: false, link_accounts: true, record: { type: 'TXT', name: '_backd-challenge.acme.example', value: 'backd-verify=abc' }, created_at: 'x' }

/** The last request a mock saw. @param {{ calls: Array<any> }} m */
const last = (m) => m.calls[m.calls.length - 1]

/** A client that is signed in and holds a fresh proof, so calls that ask for one go through. */
/** @param {Parameters<typeof mockFetch>[0]} answers */
async function signedIn(answers) {
  const m = mockFetch(answers)
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  await c.auth.signedIn(/** @type {any} */ ({ token: 'bds_t', session_id: 's', expires_at: '2099-01-01T00:00:00Z' }))
  return { c, m }
}

test('the domains of an organization are listed, claimed, checked, changed and released', async () => {
  const { c, m } = await signedIn([
    { body: { items: [claim], max_domains: 10 } },
    { status: 201, body: claim },
    { body: { ...claim, state: 'pending', reason: 'record_not_found' } },
    { body: { ...claim, state: 'verified', verified_at: 'y' } },
    { body: { ...claim, discoverable: true, link_accounts: false } },
    { status: 204 },
  ])
  const d = c.orgs.domains('o1')
  assert.equal((await d.list()).max_domains, 10)
  assert.equal(last(m).url.pathname, '/v1/acme/_orgs/o1/domains')
  const made = await d.add('acme.example')
  assert.equal(made.record.name, '_backd-challenge.acme.example')
  assert.deepEqual(last(m).body, { domain: 'acme.example' })
  assert.equal((await d.verify('acme.example')).reason, 'record_not_found')
  assert.equal(last(m).url.pathname, '/v1/acme/_orgs/o1/domains/acme.example/verify')
  assert.equal((await d.verify('acme.example')).state, 'verified')
  const changed = await d.update('acme.example', { discoverable: true, linkAccounts: false })
  assert.equal(last(m).method, 'PATCH')
  assert.deepEqual(last(m).body, { discoverable: true, link_accounts: false })
  assert.equal(changed.link_accounts, false)
  await d.remove('acme.example')
  assert.equal(last(m).method, 'DELETE')
})

test('the errors of the domains are typed', async () => {
  const { c } = await signedIn([
    { status: 409, body: errorBody('domain_taken') },
    { status: 409, body: errorBody('domain_exists') },
    { status: 409, body: errorBody('domains_limit') },
    { status: 400, body: errorBody('validation_error') },
    { status: 404, body: errorBody('not_found') },
  ])
  const d = c.orgs.domains('o1')
  await assert.rejects(d.verify('acme.example'), DomainTakenError)
  await assert.rejects(d.add('acme.example'), DomainExistsError)
  await assert.rejects(d.add('b.example'), DomainsLimitError)
  await assert.rejects(d.add('gmail.com'), ValidationError)
  await assert.rejects(d.list(), NotFoundError)
})

test('a connection takes an issuer and domains', async () => {
  const { c, m } = await signedIn([{ body: { provider: 'oidc', client_id: 'backd', issuer: 'https://acme.okta.com', domains: ['acme.example'], dormant_identities: 2 } }])
  const v = await c.orgs.sso('o1').set('oidc', { clientId: 'backd', clientSecret: 's', issuer: 'https://acme.okta.com', domains: ['acme.example'] })
  assert.deepEqual(m.calls[0].body, { client_id: 'backd', client_secret: 's', issuer: 'https://acme.okta.com', domains: ['acme.example'] })
  assert.equal(v.dormant_identities, 2)
})
