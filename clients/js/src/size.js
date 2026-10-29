// Sizes: "10MiB", "1.5GB". The grammar is the server's (strutil.ParseSize in Go) and the Dart
// client's; clients/testdata/size-vectors.json is run by all three.

/** @typedef {'both' | 'decimal' | 'binary'} SizeFamily Which units a size takes: `decimal` (B, KB..TB, powers of 1000), `binary` (B, KiB..TiB, powers of 1024) or `both` (the default). */

const UNITS = /** @type {Record<string, bigint>} */ ({
  B: 1n, KB: 10n ** 3n, MB: 10n ** 6n, GB: 10n ** 9n, TB: 10n ** 12n,
  KiB: 1n << 10n, MiB: 1n << 20n, GiB: 1n << 30n, TiB: 1n << 40n,
})
const NAMES = { both: 'B, KB, MB, GB, TB, KiB, MiB, GiB, TiB', decimal: 'B, KB, MB, GB, TB', binary: 'B, KiB, MiB, GiB, TiB' }
const PATTERN = /^([0-9]+)(?:\.([0-9]+))?(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$/
const MAX_INPUT = 64

/**
 * The bytes a size stands for. A unit follows the number directly and is case-sensitive; a
 * decimal is fine when the result is a whole number of bytes (`0.1KiB` is not). Throws a
 * `TypeError` that names what is accepted.
 *
 * @param {string} value
 * @param {{ family?: SizeFamily, allowBytes?: boolean }} [options] `family` limits the units
 *   (default `both`); `allowBytes` also accepts a bare whole number of bytes (`"1048576"`).
 * @returns {number}
 */
export function parseSize(value, options = {}) {
  const family = options.family ?? 'both'
  const s = String(value).trim()
  const m = s.length <= MAX_INPUT ? PATTERN.exec(s) : null
  if (!m || (!m[3] && (!options.allowBytes || m[2] !== undefined))) {
    throw new TypeError(`invalid size "${value}": want ${options.allowBytes ? 'a whole number of bytes or ' : ''}a number and a unit (${NAMES[family]}), such as ${family === 'decimal' ? '128MB' : '128MiB'}`)
  }
  const unit = m[3] ?? 'B'
  const binary = unit.endsWith('iB')
  if ((family === 'decimal' && binary) || (family === 'binary' && !binary && unit !== 'B')) {
    throw new TypeError(`invalid size "${value}": ${unit} is not accepted here, use one of ${NAMES[family]}`)
  }
  const frac = m[2] ?? ''
  const scale = 10n ** BigInt(frac.length)
  const total = BigInt(m[1] + frac) * UNITS[unit]
  if (total % scale !== 0n) throw new TypeError(`invalid size "${value}": not a whole number of bytes`)
  const bytes = total / scale
  if (bytes > BigInt(Number.MAX_SAFE_INTEGER)) throw new TypeError(`invalid size "${value}": too large`)
  return Number(bytes)
}

/**
 * Bytes for people: the largest unit whose value is at least 1, up to TB or TiB, with at most
 * two decimals and no trailing zeros (`1.5 KiB`, `5 MiB`). `both` prints binary. It is for
 * display and can lose digits.
 *
 * @param {number} n
 * @param {SizeFamily} [family]
 * @returns {string}
 */
export function formatBytes(n, family = 'both') {
  const decimal = family === 'decimal'
  const base = decimal ? 1000 : 1024
  const units = decimal ? ['B', 'KB', 'MB', 'GB', 'TB'] : ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  if (n < base) return `${n} B`
  let v = n
  let i = 0
  for (; i < units.length - 1; i++) {
    if (Math.round(v * 100) / 100 < base) break
    v /= base
  }
  return `${Number((Math.round(v * 100) / 100).toFixed(2))} ${units[i]}`
}
