// A reader for the zip a personal data export is (chapter 26): it finds the entries by the
// central directory and inflates them with the browser's own DecompressionStream, so the app
// can show the README inside the package without a library. It reads what backd writes
// (stored or deflated entries); it is not a general zip reader (no zip64, no encryption).

const EOCD = 0x06054b50
const CENTRAL = 0x02014b50
const LOCAL = 0x04034b50

/**
 * @param {Uint8Array} bytes  A whole zip.
 * @returns {{ names: string[], bytes: (name: string) => Promise<Uint8Array>, text: (name: string) => Promise<string> }}
 */
export function readZip(bytes) {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const text = new TextDecoder()
  // The end-of-central-directory record is the last 22 bytes, before a comment of up to 64 KiB.
  let eocd = -1
  for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 22 - 0xffff); i--) {
    if (view.getUint32(i, true) === EOCD) {
      eocd = i
      break
    }
  }
  if (eocd < 0) throw new Error('not a zip: no end of central directory')
  const count = view.getUint16(eocd + 10, true)
  let at = view.getUint32(eocd + 16, true)
  if (count === 0xffff || at === 0xffffffff) throw new Error('zip64 archives are not supported')

  /** @type {Map<string, { method: number, size: number, offset: number }>} */
  const entries = new Map()
  for (let n = 0; n < count; n++) {
    if (view.getUint32(at, true) !== CENTRAL) throw new Error('not a zip: bad central directory')
    const method = view.getUint16(at + 10, true)
    const size = view.getUint32(at + 20, true)
    const nameLength = view.getUint16(at + 28, true)
    const extraLength = view.getUint16(at + 30, true)
    const commentLength = view.getUint16(at + 32, true)
    const offset = view.getUint32(at + 42, true)
    entries.set(text.decode(bytes.subarray(at + 46, at + 46 + nameLength)), { method, size, offset })
    at += 46 + nameLength + extraLength + commentLength
  }

  /** @param {string} name */
  const read = async (name) => {
    const e = entries.get(name)
    if (!e) throw new Error(`the zip has no ${name}`)
    if (view.getUint32(e.offset, true) !== LOCAL) throw new Error('not a zip: bad local header')
    const start = e.offset + 30 + view.getUint16(e.offset + 26, true) + view.getUint16(e.offset + 28, true)
    const data = bytes.subarray(start, start + e.size)
    if (e.method === 0) return data
    if (e.method !== 8) throw new Error(`${name} uses compression method ${e.method}, which is not supported`)
    const inflated = new Blob([/** @type {BlobPart} */ (data)]).stream().pipeThrough(new DecompressionStream('deflate-raw'))
    return new Uint8Array(await new Response(inflated).arrayBuffer())
  }
  return { names: [...entries.keys()], bytes: read, text: async (name) => text.decode(await read(name)) }
}
