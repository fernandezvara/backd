import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, OrgRequiresMFAError, OrgRoleRequiresMFAError, NotOrgMemberError, LastOrgManagerError, IdTakenError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const make = (m, extra = {}) => createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, apiKey: 'bdk_test', ...extra })

const org = { id: 'o1', name: 'Acme', slug: 'acme', status: 'active', require_mfa: false, mfa_met: true, claims: {}, created_at: 't', roles: ['owner'], effective_roles: [], teams: [] }

test('create, list, get, update, leave and delete an organization', async () => {
  const m = mockFetch([
    { status: 201, body: org },
    { body: { items: [org] } },
    { body: org },
    { body: { ...org, name: 'Acme Inc' } },
    { status: 204 },
    { status: 204 },
  ])
  const c = make(m)
  assert.equal((await c.orgs.create({ name: 'Acme', slug: 'acme' })).id, 'o1')
  assert.deepEqual(m.calls[0].body, { name: 'Acme', slug: 'acme' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_orgs')
  assert.equal((await c.orgs.list()).length, 1)
  assert.equal((await c.orgs.get('o1')).name, 'Acme')
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_orgs/o1')
  assert.equal((await c.orgs.update('o1', { name: 'Acme Inc' })).name, 'Acme Inc')
  assert.equal(m.calls[3].method, 'PATCH')
  await c.orgs.leave('o1')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/_orgs/o1/leave')
  await c.orgs.delete('o1')
  assert.equal(m.calls[5].method, 'DELETE')
})

test('members, teams and invitations hang off one organization', async () => {
  const m = mockFetch([
    { body: { items: [{ user: 'u1', roles: ['owner'] }], has_more: false } },
    { body: { user: 'u2', roles: ['editor'] } },
    { status: 204 },
    { body: { items: [{ id: 't1', name: 'Editors', roles: ['editor'] }] } },
    { status: 201, body: { id: 't1', name: 'Editors', roles: ['editor'] } },
    { body: { user: 'u2', teams: ['t1'] } },
    { status: 201, body: { id: 'i1', token: 'bdo_x', link: 'https://app.example/join?token=bdo_x' } },
    { body: { items: [{ id: 'i1', status: 'pending' }] } },
    { body: { id: 'i1', token: 'bdo_y', link: null } },
    { status: 204 },
  ])
  const c = make(m)
  assert.equal((await c.orgs.members('o1').list({ limit: 10 })).items[0].user, 'u1')
  assert.equal(m.calls[0].url.searchParams.get('limit'), '10')
  await c.orgs.members('o1').update('u2', ['editor'])
  assert.deepEqual(m.calls[1].body, { roles: ['editor'] })
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_orgs/o1/members/u2')
  await c.orgs.members('o1').remove('u2')
  assert.equal(m.calls[2].method, 'DELETE')
  assert.equal((await c.orgs.teams('o1').list())[0].name, 'Editors')
  await c.orgs.teams('o1').create({ name: 'Editors', roles: ['editor'] })
  await c.orgs.teams('o1').addMember('t1', 'u2')
  assert.equal(m.calls[5].url.pathname, '/v1/acme/_orgs/o1/teams/t1/members/u2')
  assert.equal(m.calls[5].method, 'PUT')
  const inv = await c.orgs.invitations('o1').create({ binding: 'email', email: 'a@example.com', roles: ['editor'], send: true, expiresIn: '2d', maxUses: undefined })
  assert.equal(inv.token, 'bdo_x')
  assert.deepEqual(m.calls[6].body, { binding: 'email', email: 'a@example.com', roles: ['editor'], send: true, expires_in: '2d' })
  assert.equal((await c.orgs.invitations('o1').list())[0].status, 'pending')
  assert.equal((await c.orgs.invitations('o1').newLink('i1')).token, 'bdo_y')
  assert.equal(m.calls[8].url.pathname, '/v1/acme/_orgs/o1/invitations/i1/link')
  await c.orgs.invitations('o1').revoke('i1')
  assert.equal(m.calls[9].method, 'DELETE')
})

test('team members and holders of a team role', async () => {
  const m = mockFetch([{ body: { items: [{ user: 'u2', in_team: false, roles_in_team: ['manager'], team_roles: [] }], has_more: false } }])
  const c = make(m)
  const page = await c.orgs.teams('o1').members('t1', { role: 'manager', limit: 5 })
  assert.equal(page.items[0].roles_in_team[0], 'manager')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_orgs/o1/teams/t1/members')
  assert.equal(m.calls[0].url.searchParams.get('role'), 'manager')
  assert.equal(m.calls[0].url.searchParams.get('limit'), '5')
})

test('preview and accept an invitation by its token', async () => {
  const m = mockFetch([{ body: { org: { id: 'o1', name: 'Acme' }, roles: ['editor'], teams: [], binding: 'email', expires_at: 't' } }, { body: org }])
  const c = make(m)
  const pv = await c.orgs.invitations.preview('bdo_x')
  assert.equal(pv.org.name, 'Acme')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_orgs/invitations/preview')
  assert.deepEqual(m.calls[0].body, { token: 'bdo_x' })
  assert.equal((await c.orgs.invitations.accept('bdo_x')).id, 'o1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_orgs/invitations/accept')
})

test('documents are created and listed in an organization with inOrg', async () => {
  const m = mockFetch([
    { status: 201, body: { id: 'd1', title: 'x', _meta: { org: 'o1' } } },
    { body: { items: [], limit: 50, skip: 0, has_more: false } },
    { status: 201, body: { id: 'd2' } },
  ])
  const c = make(m)
  const projects = c.db('app').collection('projects')
  await projects.inOrg('o1').create({ title: 'x' }, { idempotencyKey: 'k1' })
  assert.equal(m.calls[0].url.searchParams.get('org'), 'o1')
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'k1')
  await projects.inOrg('o1').list({ where: { title: 'x' } })
  assert.equal(m.calls[1].url.searchParams.get('org'), 'o1')
  assert.equal(m.calls[1].url.searchParams.get('where'), '{"title":"x"}')
  // The plain collection sends none, and an explicit org wins.
  await projects.create({ title: 'y' }, { org: 'o2' })
  assert.equal(m.calls[2].url.searchParams.get('org'), 'o2')
})

test('the new codes are typed errors', async () => {
  const m = mockFetch([
    { status: 403, body: errorBody('org_requires_mfa', 'second factor') },
    { status: 403, body: errorBody('not_org_member', 'not a member') },
    { status: 409, body: errorBody('last_org_manager', 'last') },
    { status: 409, body: errorBody('id_taken', 'taken') },
    { status: 403, body: errorBody('org_role_requires_mfa', 'role needs a second factor') },
  ])
  const c = make(m)
  await assert.rejects(c.orgs.get('o1'), (e) => e instanceof OrgRequiresMFAError && e.status === 403)
  await assert.rejects(c.orgs.get('o2'), (e) => e instanceof NotOrgMemberError)
  await assert.rejects(c.orgs.leave('o1'), (e) => e instanceof LastOrgManagerError && e.code === 'last_org_manager')
  await assert.rejects(c.orgs.create({ name: 'x' }), (e) => e instanceof IdTakenError)
  await assert.rejects(c.orgs.update('o1', { name: 'x' }), (e) => e instanceof OrgRoleRequiresMFAError && !(e instanceof OrgRequiresMFAError))
})
