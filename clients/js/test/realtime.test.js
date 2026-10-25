// @ts-nocheck -- a scripted server: the shapes of its fakes are the test's own
import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, SessionEndedError, RealtimeNotEnabledError } from '../src/index.js'

const enc = new TextEncoder()
const tick = (ms = 10) => new Promise((r) => setTimeout(r, ms))
const until = async (cond, what = 'condition') => {
  for (let i = 0; i < 200; i++) {
    if (cond()) return
    await tick(5)
  }
  assert.fail('timed out waiting for ' + what)
}

const doc = (id, version, fields = {}) => ({ id, _meta: { version, created_at: '2026-10-01T00:00:00.000Z' }, ...fields })

/**
 * A server: `list` answers GETs from a function, every stream opened is kept so the test can write events
 * to it, and the bodies of the stream requests are recorded.
 */
function server({ list = () => ({ items: [], limit: 100, skip: 0, has_more: false }), openStatus } = {}) {
  /** @type {Array<{ push(text: string): void, end(): void, body: any, signal?: AbortSignal }>} */
  const streams = []
  const gets = []
  const fetch = async (input, init = {}) => {
    const url = new URL(String(input))
    if (init.method === 'POST' && url.pathname.endsWith('/_realtime')) {
      const status = openStatus?.(streams.length)
      if (status) return new Response(JSON.stringify({ error: { code: status.code, message: status.code, details: status.details ?? [] } }), { status: status.status, headers: { 'Content-Type': 'application/json' } })
      let controller
      const body = new ReadableStream({ start: (c) => (controller = c) })
      const s = {
        body: JSON.parse(init.body),
        signal: init.signal,
        push: (text) => controller.enqueue(enc.encode(text)),
        end: () => controller.close(),
      }
      init.signal?.addEventListener('abort', () => {
        try {
          controller.error(new Error('aborted'))
        } catch {}
      })
      streams.push(s)
      return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
    }
    gets.push(url)
    return new Response(JSON.stringify(list(url)), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  return { fetch, streams, gets }
}

const frame = (event, data, id) => (id ? `id: ${id}\n` : '') + `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
const ready = (subs) => frame('ready', { subscriptions: subs })

const client = (srv) => createClient({ url: 'http://api.test', realm: 'shop', fetch: srv.fetch })

test('stream-first: events during the load are applied after it, ignoring versions that are not newer', async () => {
  let release
  const gate = new Promise((r) => (release = r))
  const srv = server({ list: () => ({ items: [doc('a', 2, { n: 1 }), doc('b', 1)], limit: 100, skip: 0, has_more: false }) })
  const slow = srv.fetch
  srv.fetch = async (input, init) => {
    if (init?.method !== 'POST') await gate
    return slow(input, init)
  }
  const c = client(srv)
  const live = c.realtime.subscribe([{ db: 'main', collection: 'items' }])
  await until(() => srv.streams.length === 1, 'the stream')
  srv.streams[0].push(ready(['s1']))
  await tick()
  // While the list is loading: an old version of a, a newer one of b, and a new document.
  srv.streams[0].push(frame('updated', { sub: 's1', doc: doc('a', 1, { n: 0 }) }, 't1'))
  srv.streams[0].push(frame('updated', { sub: 's1', doc: doc('b', 2, { n: 9 }) }, 't2'))
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('c', 1) }, 't3'))
  await tick()
  assert.equal(live.state.status, 'connecting')
  release()
  const state = await live.ready
  assert.deepEqual(state.items.map((d) => [d.id, d._meta.version]), [['a', 2], ['b', 2], ['c', 1]])
  assert.equal(state.items[0].n, 1, 'an older version never replaces a newer one')
  assert.equal(srv.streams.length, 1, 'one stream')
  live.close()
})

test('a live list stays sorted, bounded by its limit, and removals respect versions', async () => {
  const srv = server({ list: () => ({ items: [doc('b', 1, { at: 20 }), doc('a', 1, { at: 10 })], limit: 3, skip: 0, has_more: false }) })
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'items', load: { sort: '-at', limit: 3 } }])
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  assert.deepEqual(live.items.map((d) => d.id), ['b', 'a'])
  const changes = []
  live.on('change', (s) => changes.push(s.items.map((d) => d.id).join('')))
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('c', 1, { at: 30 }) }, 't1'))
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('d', 1, { at: 15 }) }, 't2'))
  await until(() => live.items.length === 3 && live.items[0].id === 'c')
  assert.deepEqual(live.items.map((d) => d.id), ['c', 'b', 'd'], 'a is beyond the limit of 3')
  srv.streams[0].push(frame('removed', { sub: 's1', id: 'b', version: 0 }, 't3')) // older than what we hold: ignored
  srv.streams[0].push(frame('removed', { sub: 's1', id: 'c', version: 1 }, 't4'))
  await until(() => live.items.length === 2)
  assert.deepEqual(live.items.map((d) => d.id), ['b', 'd'])
  assert.ok(changes.length >= 1)
  live.close()
})

test('a reload after a reset waits a random time within the jitter, and keeps the old list meanwhile', async () => {
  let version = 1
  const srv = server({ list: () => ({ items: [doc('a', version)], limit: 100, skip: 0, has_more: false }) })
  const waits = []
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'items' }], { jitterMs: 300, random: () => 0.5 })
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  const before = srv.gets.length
  version = 2
  const t0 = Date.now()
  srv.streams[0].push(frame('reset', { subs: ['s1'], reason: 'slow_consumer' }))
  await until(() => live.state.status === 'resyncing', 'resyncing')
  assert.equal(live.items[0]._meta.version, 1, 'the last list stays while it reloads')
  await until(() => live.items[0]._meta.version === 2, 'the reload')
  const waited = Date.now() - t0
  assert.ok(waited >= 140 && waited < 1000, `waited ${waited}ms: half of the 300ms jitter`)
  assert.equal(srv.gets.length, before + 1)
  assert.equal(live.state.status, 'live')
  live.close()
})

test('resumes after a disconnection with the last token and keeps its list', async () => {
  const srv = server({ list: () => ({ items: [doc('a', 1)], limit: 100, skip: 0, has_more: false }) })
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'items' }])
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('b', 1) }, 'token-7'))
  await until(() => live.items.length === 2)
  const gets = srv.gets.length
  srv.streams[0].end() // the connection drops
  await until(() => srv.streams.length === 2, 'a second stream')
  assert.equal(srv.streams[1].body.resume_from, 'token-7')
  assert.equal(live.items.length, 2, 'the list is kept')
  srv.streams[1].push(ready(['s1']))
  await until(() => live.state.status === 'live')
  assert.equal(srv.gets.length, gets, 'a resume replays: nothing is read again')
  live.close()
})

test('adding a subscription reopens the one stream with the previous token', async () => {
  const srv = server()
  const c = client(srv)
  const one = c.realtime.subscribe([{ db: 'main', collection: 'items' }])
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('a', 1) }, 'tok'))
  await one.ready
  const two = c.realtime.subscribe([{ db: 'main', collection: 'orders' }])
  await until(() => srv.streams.length === 2, 'the reopened stream')
  assert.deepEqual(srv.streams[1].body.subscriptions.map((s) => s.collection), ['items', 'orders'])
  assert.equal(srv.streams[1].body.resume_from, 'tok')
  assert.ok(srv.streams[0].signal.aborted, 'the old stream is closed')
  srv.streams[1].push(ready(['s1', 's2']))
  await two.ready
  one.close()
  two.close()
  await until(() => srv.streams[1].signal.aborted, 'the last stream closing with the last view')
})

test('session_ended stops the views with a typed error; reauth reconnects at once', async () => {
  const srv = server()
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'items' }])
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('a', 1) }, 'tok'))
  srv.streams[0].push(frame('reauth', {}))
  await until(() => srv.streams.length === 2, 'the reconnection')
  assert.equal(srv.streams[1].body.resume_from, 'tok')
  const errors = []
  live.on('error', (e) => errors.push(e))
  srv.streams[1].push(ready(['s1']))
  srv.streams[1].push(frame('session_ended', {}))
  await until(() => errors.length === 1)
  assert.ok(errors[0] instanceof SessionEndedError)
  assert.equal(live.state.status, 'closed')
})

test('a collection that is not realtime fails the view with a typed error', async () => {
  const srv = server({ openStatus: () => ({ status: 400, code: 'realtime_not_enabled', details: [{ path: 'subscriptions[0].collection', reason: 'realtime is not enabled' }] }) })
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'plain' }])
  await assert.rejects(live.ready, RealtimeNotEnabledError)
})

test('referenced documents are read in one batched call and kept beside the list', async () => {
  const srv = server({
    list: (url) => {
      if (url.pathname.endsWith('/profiles')) {
        const where = JSON.parse(url.searchParams.get('where'))
        return { items: where.id.$in.map((id) => ({ id, _meta: { version: 1 }, name: 'N-' + id, secret: 'x' })), limit: 100, skip: 0, has_more: false }
      }
      return { items: [doc('i1', 1, { author: 'p1' }), doc('i2', 1, { author: 'p2' }), doc('i3', 1, { author: 'p1' })], limit: 100, skip: 0, has_more: false }
    },
  })
  const live = client(srv).realtime.subscribe([
    { db: 'main', collection: 'items', references: { author: { field: 'author', collection: 'profiles', fields: ['name'] } } },
  ])
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  await until(() => Object.keys(live.state.references.author ?? {}).length === 2, 'the references')
  assert.deepEqual(live.state.references.author.p1, { id: 'p1', _meta: { version: 1 }, name: 'N-p1' }, 'only the asked fields are kept')
  const refReads = srv.gets.filter((u) => u.pathname.endsWith('/profiles'))
  assert.equal(refReads.length, 1, 'one batched read')
  // A new document with a known author costs nothing; one with a new author asks only for that one.
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('i4', 1, { author: 'p1' }) }, 't1'))
  srv.streams[0].push(frame('created', { sub: 's1', doc: doc('i5', 1, { author: 'p3' }) }, 't2'))
  await until(() => 'p3' in (live.state.references.author ?? {}))
  const reads = srv.gets.filter((u) => u.pathname.endsWith('/profiles'))
  assert.equal(reads.length, 2)
  assert.deepEqual(JSON.parse(reads[1].searchParams.get('where')), { id: { $in: ['p3'] } })
  live.close()
})

test('a collection scoped to a team subscribes with its _meta.team', async () => {
  const srv = server()
  const live = client(srv).db('main').collection('items').inTeam('t_1').subscribe({ where: { done: false } })
  await until(() => srv.streams.length === 1)
  assert.deepEqual(srv.streams[0].body.subscriptions[0].where, { done: false, '_meta.team': 't_1' })
  live.close()
})

test('a reload that finds the access gone empties the list and keeps the view', async () => {
  let forbidden = false
  const srv = server({ list: () => ({ items: [doc('a', 1)], limit: 100, skip: 0, has_more: false }) })
  const plain = srv.fetch
  srv.fetch = async (input, init) => {
    if (forbidden && init?.method !== 'POST') {
      return new Response(JSON.stringify({ error: { code: 'forbidden', message: 'no read rule' } }), { status: 403, headers: { 'Content-Type': 'application/json' } })
    }
    return plain(input, init)
  }
  const live = client(srv).realtime.subscribe([{ db: 'main', collection: 'items' }], { jitterMs: 1 })
  await until(() => srv.streams.length === 1)
  srv.streams[0].push(ready(['s1']))
  await live.ready
  assert.equal(live.items.length, 1)
  const lost = []
  live.on('access_lost', (e) => lost.push(e))
  forbidden = true
  srv.streams[0].push(frame('reset', { subs: ['s1'], reason: 'access_changed' }))
  await until(() => lost.length === 1, 'access_lost')
  assert.deepEqual(live.items, [])
  assert.equal(live.state.status, 'live')
  live.close()
})
