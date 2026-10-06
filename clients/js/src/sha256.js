// SHA-256, incrementally: a direct upload declares the file's checksum before any byte is
// sent, and a file can be larger than anything worth holding in memory. WebCrypto only
// hashes a whole buffer, so this reads the file a chunk at a time.

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74,
  0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d,
  0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e,
  0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
  0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

/** @param {number} x @param {number} n */
const rotr = (x, n) => (x >>> n) | (x << (32 - n))

/** An incremental SHA-256: `update` with chunks, then `hex()` once. */
export class Sha256 {
  constructor() {
    this.h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
    this.block = new Uint8Array(64)
    this.filled = 0
    this.length = 0 // bytes
    this.w = new Uint32Array(64)
  }

  /** @param {Uint8Array} data */
  update(data) {
    this.length += data.length
    let i = 0
    if (this.filled > 0) {
      const take = Math.min(64 - this.filled, data.length)
      this.block.set(data.subarray(0, take), this.filled)
      this.filled += take
      i = take
      if (this.filled < 64) return this
      this.compress(this.block)
      this.filled = 0
    }
    for (; i + 64 <= data.length; i += 64) this.compress(data.subarray(i, i + 64))
    if (i < data.length) {
      this.block.set(data.subarray(i), 0)
      this.filled = data.length - i
    }
    return this
  }

  /** @param {Uint8Array} b */
  compress(b) {
    const w = this.w
    for (let t = 0; t < 16; t++) w[t] = (b[t * 4] << 24) | (b[t * 4 + 1] << 16) | (b[t * 4 + 2] << 8) | b[t * 4 + 3]
    for (let t = 16; t < 64; t++) {
      const s0 = rotr(w[t - 15], 7) ^ rotr(w[t - 15], 18) ^ (w[t - 15] >>> 3)
      const s1 = rotr(w[t - 2], 17) ^ rotr(w[t - 2], 19) ^ (w[t - 2] >>> 10)
      w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0
    }
    let [a, b2, c, d, e, f, g, h] = this.h
    for (let t = 0; t < 64; t++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)
      const ch = (e & f) ^ (~e & g)
      const t1 = (h + S1 + ch + K[t] + w[t]) | 0
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)
      const maj = (a & b2) ^ (a & c) ^ (b2 & c)
      const t2 = (S0 + maj) | 0
      h = g
      g = f
      f = e
      e = (d + t1) | 0
      d = c
      c = b2
      b2 = a
      a = (t1 + t2) | 0
    }
    const H = this.h
    H[0] += a
    H[1] += b2
    H[2] += c
    H[3] += d
    H[4] += e
    H[5] += f
    H[6] += g
    H[7] += h
  }

  /** The digest as 64 lower-case hex characters. Call once. */
  hex() {
    const bits = this.length * 8
    const pad = new Uint8Array(((this.filled < 56 ? 56 : 120) - this.filled) + 8)
    pad[0] = 0x80
    const view = new DataView(pad.buffer)
    view.setUint32(pad.length - 8, Math.floor(bits / 2 ** 32))
    view.setUint32(pad.length - 4, bits >>> 0)
    const length = this.length
    this.update(pad)
    this.length = length
    return [...this.h].map((x) => (x >>> 0).toString(16).padStart(8, '0')).join('')
  }
}

/**
 * The SHA-256 of a Blob or File, read a chunk at a time.
 * @param {Blob} blob
 * @param {{ signal?: AbortSignal }} [opts]
 * @returns {Promise<string>}
 */
export async function sha256Blob(blob, opts = {}) {
  const h = new Sha256()
  const reader = blob.stream().getReader()
  for (;;) {
    if (opts.signal?.aborted) {
      await reader.cancel()
      throw opts.signal.reason ?? new DOMException('aborted', 'AbortError')
    }
    const { done, value } = await reader.read()
    if (done) break
    h.update(value)
  }
  return h.hex()
}
