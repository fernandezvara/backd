import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parseSize, formatBytes } from '../src/index.js'

// Shared with the Go and Dart tests: one grammar in three languages.
const vectors = JSON.parse(readFileSync(new URL('../../testdata/size-vectors.json', import.meta.url), 'utf8'))

test('parseSize follows the shared vectors', () => {
  for (const c of vectors.parse) {
    const opts = { family: c.family, allowBytes: c.allowBytes }
    if (c.error) assert.throws(() => parseSize(c.in, opts), TypeError, `${JSON.stringify(c.in)} (${c.family}, ${c.allowBytes})`)
    else assert.equal(parseSize(c.in, opts), c.bytes, `${JSON.stringify(c.in)} (${c.family}, ${c.allowBytes})`)
  }
})

test('formatBytes follows the shared vectors', () => {
  for (const c of vectors.format) assert.equal(formatBytes(c.bytes, c.family), c.out, `${c.bytes} ${c.family}`)
})

test('errors name what the field takes', () => {
  assert.throws(() => parseSize('2MB', { family: 'binary' }), /KiB, MiB, GiB, TiB/)
  assert.throws(() => parseSize('2MiB', { family: 'decimal' }), /KB, MB, GB, TB/)
  assert.throws(() => parseSize('0.1KiB'), /whole number of bytes/)
})
