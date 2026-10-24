import { test } from 'node:test'
import assert from 'node:assert/strict'
import { deflateRawSync } from 'node:zlib'

import { readZip } from '../examples/shelf/lib/zip.js'

/** A zip with stored and deflated entries, as the central directory describes them. */
function makeZip(/** @type {Array<[string, string, 'store' | 'deflate']>} */ entries, comment = '') {
  const parts = []
  const central = []
  let offset = 0
  for (const [name, content, how] of entries) {
    const raw = Buffer.from(content)
    const data = how === 'deflate' ? deflateRawSync(raw) : raw
    const nameBytes = Buffer.from(name)
    const local = Buffer.alloc(30)
    local.writeUInt32LE(0x04034b50, 0)
    local.writeUInt16LE(how === 'deflate' ? 8 : 0, 8)
    local.writeUInt32LE(data.length, 18)
    local.writeUInt32LE(raw.length, 22)
    local.writeUInt16LE(nameBytes.length, 26)
    parts.push(local, nameBytes, data)
    const c = Buffer.alloc(46)
    c.writeUInt32LE(0x02014b50, 0)
    c.writeUInt16LE(how === 'deflate' ? 8 : 0, 10)
    c.writeUInt32LE(data.length, 20)
    c.writeUInt32LE(raw.length, 24)
    c.writeUInt16LE(nameBytes.length, 28)
    c.writeUInt32LE(offset, 42)
    central.push(c, nameBytes)
    offset += local.length + nameBytes.length + data.length
  }
  const dir = Buffer.concat(central)
  const end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054b50, 0)
  end.writeUInt16LE(entries.length, 8)
  end.writeUInt16LE(entries.length, 10)
  end.writeUInt32LE(dir.length, 12)
  end.writeUInt32LE(offset, 16)
  end.writeUInt16LE(Buffer.byteLength(comment), 20)
  return new Uint8Array(Buffer.concat([...parts, dir, end, Buffer.from(comment)]))
}

test('reads stored and deflated entries by name', async () => {
  const zip = readZip(makeZip([
    ['manifest.json', '{"format":1}', 'store'],
    ['README.html', '<h1>Your data</h1>'.repeat(50), 'deflate'],
    ['app/_files/fl_1', 'PNG', 'store'],
  ]))
  assert.deepEqual(zip.names, ['manifest.json', 'README.html', 'app/_files/fl_1'])
  assert.equal(await zip.text('manifest.json'), '{"format":1}')
  assert.equal(await zip.text('README.html'), '<h1>Your data</h1>'.repeat(50))
  assert.deepEqual([...(await zip.bytes('app/_files/fl_1'))], [80, 78, 71])
})

test('finds the directory behind a comment, and refuses what it cannot read', async () => {
  assert.equal(await readZip(makeZip([['a.txt', 'hello', 'deflate']], 'a comment after the directory')).text('a.txt'), 'hello')
  assert.throws(() => readZip(new Uint8Array(100)), /not a zip/)
  await assert.rejects(readZip(makeZip([['a.txt', 'x', 'store']])).text('missing.txt'), /no missing\.txt/)
})
