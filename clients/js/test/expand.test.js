import test from 'node:test'
import assert from 'node:assert/strict'
import { createClient, expand, InvalidExpandError, ExpandTooLargeError, ValidationError } from '../src/index.js'
import { mockFetch } from './helpers.js'

const meta = { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z' }
const post = { id: 'p1', title: 'Hello', _meta: meta, _expanded: { author: { id: 'u1', name: 'Ada' }, tags: [{ id: 't1' }, null] } }

test('expand builds the parameter from names and field selections', () => {
  assert.equal(expand('author'), 'author')
  assert.equal(expand(['author', ['name', 'avatar']], 'tags'), 'author(name,avatar),tags')
  assert.equal(expand('author.company', ['author.company', ['address.city']]), 'author.company,author.company(address.city)')
  assert.equal(expand(), '')
  for (const bad of /** @type {any[][]} */ ([['au thor'], ['a,b'], ['a(b)'], [['author', []]], [['author', ['na me']]], [['author', 'name']], [7]])) {
    assert.throws(() => expand(...bad), TypeError, JSON.stringify(bad))
  }
})

test('get and list send expand only when given', async () => {
  const f = mockFetch([
    { body: post },
    { body: { items: [post], limit: 20, skip: 0, has_more: false } },
    { body: post },
    { body: { items: [], limit: 20, skip: 0, has_more: false } },
  ])
  const posts = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch }).db('blog').collection('posts')
  const got = await posts.get('p1', { expand: expand(['author', ['name']], 'tags') })
  assert.equal(new URL(f.calls[0].url).searchParams.get('expand'), 'author(name),tags')
  assert.equal(got._expanded?.author?.name, 'Ada')
  const page = await posts.list({ where: { published: true }, expand: 'author' })
  const q = new URL(f.calls[1].url).searchParams
  assert.equal(q.get('expand'), 'author')
  assert.equal(q.get('where'), '{"published":true}')
  assert.equal(page.items[0]._expanded?.tags?.length, 2)

  // Nothing implicit: plain reads, helpers and lists never carry it.
  await posts.get('p1')
  await posts.list({ limit: 5 })
  assert.equal(new URL(f.calls[2].url).searchParams.has('expand'), false)
  assert.equal(new URL(f.calls[3].url).searchParams.has('expand'), false)
})

test('iterate carries expand to every page', async () => {
  const f = mockFetch([
    { body: { items: [post], limit: 1, skip: 0, has_more: true, next_cursor: 'c1' } },
    { body: { items: [post], limit: 1, skip: 0, has_more: false } },
  ])
  const posts = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch }).db('blog').collection('posts')
  let n = 0
  for await (const d of posts.iterate({ limit: 1, expand: 'author' })) n += d._expanded ? 1 : 0
  assert.equal(n, 2)
  assert.deepEqual(f.calls.map((c) => new URL(c.url).searchParams.get('expand')), ['author', 'author'])
})

test('invalid_expand and expand_too_large are typed errors', async () => {
  const f = mockFetch([
    { status: 400, body: { error: { code: 'invalid_expand', message: 'invalid expand: "nope" is not a reference of posts', details: [{ path: 'expand', reason: '"nope" is not a reference of posts' }], request_id: 'r1' } } },
    { status: 400, body: { error: { code: 'expand_too_large', message: 'this expansion would return 2100 documents', request_id: 'r2' } } },
  ])
  const posts = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch }).db('blog').collection('posts')
  await assert.rejects(posts.get('p1', { expand: 'nope' }), (e) => {
    assert.ok(e instanceof InvalidExpandError && e instanceof ValidationError)
    assert.equal(e.code, 'invalid_expand')
    assert.equal(e.details[0].path, 'expand')
    return true
  })
  await assert.rejects(posts.list({ expand: 'author', limit: 100 }), (e) => e instanceof ExpandTooLargeError && e.status === 400)
})
