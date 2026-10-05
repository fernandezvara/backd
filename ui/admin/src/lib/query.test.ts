import { expect, test } from 'vitest'
import { buildWhere, operatorsFor, orderBy, queryFields, QueryError } from './query'

const schema = {
  type: 'object',
  properties: {
    title: { type: 'string' },
    price: { type: 'number' },
    qty: { type: 'integer' },
    live: { type: 'boolean' },
    starts: { type: 'string', format: 'date-time', 'x-backd-store': 'date' },
    status: { enum: ['draft', 'sent'] },
    tags: { type: 'array', items: { type: 'string' } },
    author: { type: 'object', properties: { email: { type: 'string' } } },
    lines: { type: 'array', items: { type: 'object', properties: { sku: { type: 'string' } } } },
    blob: { type: 'object' },
  },
}

test('declared fields, nested and in arrays, plus the document fields', () => {
  const fields = queryFields(schema)
  const byPath = Object.fromEntries(fields.map((f) => [f.path, f.type]))
  expect(byPath).toMatchObject({ id: 'string', title: 'string', price: 'number', qty: 'integer', live: 'boolean', starts: 'date', status: 'enum', tags: 'string', 'author.email': 'string', 'lines.sku': 'string', '_meta.created_at': 'date', '_meta.version': 'integer' })
  expect(byPath.blob).toBeUndefined() // JSON-only fields aren't queryable here
})

test('operators follow the type', () => {
  expect(operatorsFor('string')).toContain('$icontains')
  expect(operatorsFor('date')).not.toContain('$icontains') // text operators are refused on stored dates
  expect(operatorsFor('date')).toContain('$between')
  expect(operatorsFor('boolean')).toEqual(['$eq', '$ne', '$isNull', '$notNull'])
  expect(operatorsFor('number')).not.toContain('$like')
})

test('conditions become a where, typed', () => {
  const fields = queryFields(schema)
  expect(
    buildWhere(
      [
        { field: 'title', op: '$icontains', value: 'go' },
        { field: 'price', op: '$gte', value: '10' },
        { field: 'price', op: '$lt', value: '50' },
        { field: 'live', op: '$eq', value: 'true' },
        { field: 'qty', op: '$in', value: '1, 2,3' },
        { field: 'qty', op: '$between', value: '1, 5' },
        { field: 'status', op: '$isNull', value: 'false' },
        { field: '_meta.created_at', op: '$gte', value: '2026-09-01T00:00' },
      ],
      fields,
    ),
  ).toEqual({
    title: { $icontains: 'go' },
    price: { $gte: 10, $lt: 50 },
    live: { $eq: true },
    qty: { $in: [1, 2, 3], $between: [1, 5] },
    status: { $isNull: false },
    '_meta.created_at': { $gte: new Date('2026-09-01T00:00').toISOString() },
  })
})

test('what cannot be built is said, not sent', () => {
  const fields = queryFields(schema)
  expect(() => buildWhere([{ field: 'price', op: '$gt', value: 'abc' }], fields)).toThrow(QueryError)
  expect(() => buildWhere([{ field: 'qty', op: '$eq', value: '1.5' }], fields)).toThrow('not an integer')
  expect(() => buildWhere([{ field: 'live', op: '$like', value: 'x' }], fields)).toThrow("can't be used")
  expect(() => buildWhere([{ field: 'nope', op: '$eq', value: 'x' }], fields)).toThrow('unknown field')
  expect(() => buildWhere([{ field: 'qty', op: '$between', value: '1' }], fields)).toThrow('two values')
  expect(() => buildWhere([{ field: 'qty', op: '$in', value: ' , ' }], fields)).toThrow('at least one')
})

test('order', () => {
  expect(orderBy('title', 'asc')).toBe('title')
  expect(orderBy('title', 'desc')).toBe('-title')
  expect(orderBy('', 'asc')).toBeUndefined()
})
