import { expect, test } from 'vitest'
import { errorsByPath, formable, initialValue, isoToLocalInput, localInputToIso, newDocument, resolve } from './schema'

const root = {
  type: 'object',
  properties: {
    title: { type: 'string', minLength: 1 },
    pinned: { type: 'boolean' },
    tags: { type: 'array', items: { type: 'string' } },
    author: { type: 'object', properties: { name: { type: 'string' } }, required: ['name'] },
    kind: { enum: ['a', 'b'] },
    note: { type: ['string', 'null'] },
    meta: { type: 'object' },
    either: { oneOf: [{ type: 'string' }, { type: 'number' }] },
    node: { $ref: '#/$defs/node' },
    addr: { $ref: '#/$defs/addr' },
  },
  required: ['title', 'author'],
  $defs: {
    node: { type: 'object', properties: { next: { $ref: '#/$defs/node' } } },
    addr: { type: 'object', properties: { city: { type: 'string' } } },
  },
}

test('kinds', () => {
  const p = root.properties
  expect(resolve(p.title, root).kind).toBe('string')
  expect(resolve(p.pinned, root).kind).toBe('boolean')
  expect(resolve(p.tags, root).kind).toBe('array')
  expect(resolve(p.author, root).kind).toBe('object')
  expect(resolve(p.kind, root).kind).toBe('enum')
  expect(resolve(p.note, root)).toMatchObject({ kind: 'string', nullable: true })
  expect(resolve(p.addr, root).kind).toBe('object') // a local $ref is followed
})

test('what a form cannot draw falls back to JSON, with the reason', () => {
  const p = root.properties
  expect(resolve(p.meta, root)).toMatchObject({ kind: 'json', reason: 'free-form object' })
  expect(resolve(p.either, root)).toMatchObject({ kind: 'json', reason: 'oneOf' })
  expect(resolve({ anyOf: [] }, root).kind).toBe('json')
  expect(resolve({ if: {}, then: {} }, root).kind).toBe('json')
  expect(resolve({ type: ['string', 'number'] }, root).kind).toBe('json')
  expect(resolve({ $ref: '#/$defs/missing' }, root)).toMatchObject({ kind: 'json', reason: 'unresolved $ref' })
})

test('a recursive $ref is not followed', () => {
  // node → next → node …: the first level draws, the cycle is reported.
  const node = resolve(root.properties.node, root)
  expect(node.kind).toBe('object')
  expect(resolve((node.schema.properties as Record<string, object>).next, root, ['#/$defs/node'])).toMatchObject({ kind: 'json', reason: 'recursive $ref' })
})

test('the whole document is a form only when the root is an object with properties', () => {
  expect(formable(root)).toBe(true)
  expect(formable({ oneOf: [] })).toBe(false)
  expect(formable({ type: 'object' })).toBe(false)
  expect(formable(undefined)).toBe(false)
})

test('a new document starts with its required fields only', () => {
  expect(newDocument(root)).toEqual({ title: '', author: { name: '' } })
  expect(initialValue({ type: 'integer', default: 3 }, root)).toBe(3)
  expect(initialValue({ type: 'number' }, root)).toBeUndefined()
})

test('date-time inputs round-trip', () => {
  const iso = '2026-10-05T07:30:00.000Z'
  expect(localInputToIso(isoToLocalInput(iso))).toBe(iso)
  expect(isoToLocalInput('nope')).toBe('')
  expect(localInputToIso('')).toBe('')
})

test('server errors are keyed by path', () => {
  expect(errorsByPath([{ path: 'author.name', reason: 'is required' }, { path: 'author.name', reason: 'too short' }, { path: 'title', reason: 'x' }])).toEqual({
    'author.name': 'is required; too short',
    title: 'x',
  })
})
