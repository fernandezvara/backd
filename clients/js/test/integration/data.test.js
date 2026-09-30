import { test } from 'node:test'
import assert from 'node:assert/strict'

import { AuthenticationError, ConflictError, ForbiddenError, NotFoundError, ValidationError, VersionMismatchError } from '../../src/index.js'
import { apiKey, client, skip, user } from './env.js'

/** @typedef {{ title: string, slug?: string, status?: string, published?: boolean, members?: string[] }} Post */

test('access rules through the client', { skip }, async () => {
  const ada = await user('ada')
  const bob = await user('bob')
  /** @param {import('../../src/index.js').Client} c */
  const posts = (c) => c.db('app').collection('posts')
  const tag = Math.random().toString(36).slice(2)

  const pub = await posts(ada.c).create({ title: 'public', published: true, status: tag })
  const draft = await posts(ada.c).create({ title: 'draft', published: false, status: tag })
  const shared = await posts(ada.c).create({ title: 'shared', members: [bob.id], status: tag })
  assert.equal(pub._meta.owner, ada.id)
  assert.equal(pub._meta.created_by, 'user:' + ada.id)
  assert.equal(pub._meta.version, 1)

  const titles = async (/** @type {import('../../src/index.js').Client} */ c) =>
    (await posts(c).list({ where: { status: tag }, orderBy: 'title', count: true })).items.map((d) => d.title)
  assert.deepEqual(await titles(client()), ['public'])
  assert.deepEqual(await titles(ada.c), ['draft', 'public', 'shared'])
  assert.deepEqual(await titles(bob.c), ['public', 'shared'])

  // $or narrows, but never widens what the rules allow.
  const either = { status: tag, $or: [{ published: true }, { published: false }, { title: 'shared' }] }
  const orTitles = async (/** @type {import('../../src/index.js').Client} */ c) =>
    (await posts(c).list({ where: either, orderBy: 'title' })).items.map((d) => d.title)
  assert.deepEqual(await orTitles(client()), ['public'])
  assert.deepEqual(await orTitles(bob.c), ['public', 'shared'])
  const narrowed = await posts(ada.c).list({ where: { status: tag, $or: [{ title: 'draft' }, { title: 'shared' }] }, orderBy: 'title' })
  assert.deepEqual(narrowed.items.map((d) => d.title), ['draft', 'shared'])

  await assert.rejects(posts(bob.c).get(draft.id), NotFoundError)
  assert.equal((await posts(bob.c).get(shared.id)).title, 'shared')
  await assert.rejects(posts(client()).create({ title: 'anon' }), AuthenticationError)

  // Owners edit, but not the status; others can't; admins delete.
  const edited = await posts(ada.c).patch(draft.id, { title: 'draft 2' })
  assert.equal(edited._meta.version, 2)
  await assert.rejects(posts(ada.c).patch(draft.id, { status: 'x' }), ForbiddenError)
  await assert.rejects(posts(bob.c).patch(shared.id, { title: 'mine' }), ForbiddenError)
  await assert.rejects(posts(bob.c).patch(draft.id, { title: 'mine' }), NotFoundError)
  await assert.rejects(posts(ada.c).delete(draft.id), ForbiddenError)

  const svc = client({ apiKey })
  assert.equal((await posts(svc).list({ where: { status: tag }, count: true })).total, 3, 'API keys bypass rules')
  await posts(svc).delete(draft.id)
  await assert.rejects(posts(ada.c).get(draft.id), NotFoundError)
})

test('versions, If-Match and replace', { skip }, async () => {
  const { c } = await user('ver')
  const posts = c.db('app').collection('posts')
  const d = await posts.create({ title: 'v1' })
  const v2 = await posts.patch(d.id, { title: 'v2' }, { ifMatch: 1 })
  assert.equal(v2._meta.version, 2)
  await assert.rejects(posts.patch(d.id, { title: 'stale' }, { ifMatch: 1 }), VersionMismatchError)
  const replaced = await posts.replace(d.id, { title: 'replaced' }, { ifMatch: 2 })
  assert.equal(replaced.title, 'replaced')
  assert.equal(replaced._meta.version, 3)
  // Merge patch: null removes a field.
  const withSlug = await posts.patch(d.id, { slug: 'x-' + d.id })
  assert.equal(withSlug.slug, 'x-' + d.id)
  const without = await posts.patch(d.id, { slug: null })
  assert.equal('slug' in without, false)
})

test('pagination and iterate', { skip }, async () => {
  const { c } = await user('pages')
  const posts = c.db('app').collection('posts')
  const tag = 'p-' + Math.random().toString(36).slice(2)
  for (let i = 0; i < 5; i++) await posts.create({ title: 't' + i, status: tag })

  const first = await posts.list({ where: { status: tag }, orderBy: 'title', limit: 2, count: true })
  assert.deepEqual(first.items.map((d) => d.title), ['t0', 't1'])
  assert.equal(first.has_more, true)
  assert.equal(first.total, 5)

  /** @type {string[]} */
  const all = []
  for await (const d of posts.iterate({ where: { status: tag }, orderBy: '-title', limit: 2 })) all.push(d.title)
  assert.deepEqual(all, ['t4', 't3', 't2', 't1', 't0'])
})

test('validation, unique values and missing collections', { skip }, async () => {
  const { c } = await user('errs')
  const posts = c.db('app').collection('posts')
  await assert.rejects(posts.create({ title: '' }), (/** @type {any} */ e) => {
    assert.ok(e instanceof ValidationError)
    assert.equal(e.code, 'validation_error')
    assert.equal(e.details[0].path, 'title')
    assert.ok(e.requestId)
    return true
  })
  const tags = c.db('app').collection('tags')
  const name = 'unique-' + Math.random().toString(36).slice(2)
  await tags.create({ name })
  await assert.rejects(tags.create({ name }), (/** @type {any} */ e) => e instanceof ConflictError && e.code === 'conflict' && e.details[0].path === 'name')
  await assert.rejects(c.db('app').collection('nope').list(), NotFoundError)
  await assert.rejects(posts.list({ where: { nope: 1 } }), (/** @type {any} */ e) => e instanceof ValidationError && e.code === 'invalid_query')
  // A collection without rules.yaml: users can't, API keys can.
  await assert.rejects(c.db('app').collection('private').list(), ForbiddenError)
  await client({ apiKey }).db('app').collection('private').list()
})

test('batch: atomic writes across collections, and a version mismatch aborts the whole batch', { skip }, async () => {
  const { c } = await user('batch')
  const db = c.db('app')
  const tag = Math.random().toString(36).slice(2)

  const [post, created] = await db.batch([
    { op: 'create', collection: 'posts', document: { title: 'batched', status: tag } },
    { op: 'create', collection: 'tags', document: { name: 'batch-' + tag } },
  ])
  assert.equal(/** @type {any} */ (post).title, 'batched')
  assert.equal(/** @type {any} */ (created).name, 'batch-' + tag)

  // A version mismatch on the second operation rejects the whole batch;
  // the first operation's create never happened.
  await assert.rejects(
    db.batch([
      { op: 'create', collection: 'posts', document: { title: 'never', status: tag } },
      { op: 'patch', collection: 'tags', id: /** @type {any} */ (created).id, patch: { name: 'x' }, ifMatch: 99 },
    ]),
    VersionMismatchError,
  )
  const posts = await db.collection('posts').list({ where: { status: tag } })
  assert.deepEqual(
    posts.items.map((p) => p.title),
    ['batched'],
  )
})
