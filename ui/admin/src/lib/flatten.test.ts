import { expect, test } from 'vitest'
import { flatten } from './flatten'

test('nested settings become dotted rows', () => {
  expect(flatten({ auth: 'enabled', sessions: { idle_timeout: '30m', cookie: { enabled: false } }, cors: { origins: ['a', 'b'] }, empty: {} })).toEqual([
    { path: 'auth', value: 'enabled' },
    { path: 'sessions.idle_timeout', value: '30m' },
    { path: 'sessions.cookie.enabled', value: 'false' },
    { path: 'cors.origins', value: 'a, b' },
    { path: 'empty', value: '' },
  ])
})

test('lists of objects get one row per item', () => {
  expect(flatten({ x: [{ a: 1 }, { a: 2 }] })).toEqual([
    { path: 'x[0].a', value: '1' },
    { path: 'x[1].a', value: '2' },
  ])
})
