/**
 * @typedef {'day' | 'week' | 'month' | 'year'} BucketUnit
 */

const KEY = {
  day: /^\d{4}-\d{2}-\d{2}$/,
  week: /^\d{4}-W\d{2}$/,
  month: /^\d{4}-\d{2}$/,
  year: /^\d{4}$/,
}

const pad = (/** @type {number} */ n, w = 2) => String(n).padStart(w, '0')

/**
 * The ISO 8601 week-year and week of a UTC date (weeks start on Monday; week 1 holds January 4th).
 * @param {Date} d
 */
function isoWeek(d) {
  const t = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()))
  const day = t.getUTCDay() || 7
  t.setUTCDate(t.getUTCDate() + 4 - day) // the Thursday of this week decides the year
  const yearStart = Date.UTC(t.getUTCFullYear(), 0, 1)
  return { year: t.getUTCFullYear(), week: Math.ceil(((t.getTime() - yearStart) / 86400000 + 1) / 7) }
}

/**
 * The bucket key of a UTC date, in the text form the server uses.
 * @param {Date} d
 * @param {BucketUnit} unit
 */
export function bucketKey(d, unit) {
  switch (unit) {
    case 'day':
      return `${pad(d.getUTCFullYear(), 4)}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`
    case 'week': {
      const { year, week } = isoWeek(d)
      return `${pad(year, 4)}-W${pad(week)}`
    }
    case 'month':
      return `${pad(d.getUTCFullYear(), 4)}-${pad(d.getUTCMonth() + 1)}`
    case 'year':
      return pad(d.getUTCFullYear(), 4)
  }
  throw new TypeError(`backd: unknown bucket unit ${JSON.stringify(unit)}`)
}

/**
 * The first instant (UTC) of the bucket a key names.
 * @param {string} key
 * @param {BucketUnit} unit
 */
function keyStart(key, unit) {
  switch (unit) {
    case 'day':
      return new Date(`${key}T00:00:00Z`)
    case 'month':
      return new Date(`${key}-01T00:00:00Z`)
    case 'year':
      return new Date(`${key}-01-01T00:00:00Z`)
    case 'week': {
      const [y, w] = key.split('-W').map(Number)
      const jan4 = new Date(Date.UTC(y, 0, 4))
      const monday = new Date(jan4.getTime() - ((jan4.getUTCDay() || 7) - 1) * 86400000)
      return new Date(monday.getTime() + (w - 1) * 7 * 86400000)
    }
  }
  throw new TypeError(`backd: unknown bucket unit ${JSON.stringify(unit)}`)
}

/**
 * @param {BucketUnit} unit
 * @param {Date} d
 */
function next(unit, d) {
  const n = new Date(d)
  switch (unit) {
    case 'day':
      n.setUTCDate(n.getUTCDate() + 1)
      break
    case 'week':
      n.setUTCDate(n.getUTCDate() + 7)
      break
    case 'month':
      n.setUTCMonth(n.getUTCMonth() + 1)
      break
    case 'year':
      n.setUTCFullYear(n.getUTCFullYear() + 1)
      break
  }
  return n
}

/**
 * @param {string | Date} v
 * @param {BucketUnit} unit
 * @param {string} what
 */
function toKey(v, unit, what) {
  if (typeof v === 'string' && KEY[unit].test(v)) return v
  const d = v instanceof Date ? v : new Date(v)
  if (Number.isNaN(d.getTime())) throw new TypeError(`backd: ${what} is not a date or a ${unit} key like ${bucketKey(new Date(0), unit)}`)
  return bucketKey(d, unit)
}

/**
 * Fills the buckets an aggregation left out, so a chart has a point for every day, week, month
 * or year between `from` and `to` (inclusive). The server doesn't return empty buckets; this
 * adds them to groups grouped by one date field and nothing else (`groupBy: 'paid_at:month'`).
 * A bucket with no group gets `count: 0` and the values of `empty` (for sums, `{ sum_total: '0' }`).
 *
 * `from` and `to` are bucket keys in the zone of the aggregation (`'2026-01'`), or dates, which
 * name the bucket they fall in as seen in UTC; the groups are returned in key order. A `null`
 * group (documents with no date) is kept at the front.
 * @template {{ key: Record<string, any>, [metric: string]: any }} G
 * @param {G[]} groups
 * @param {{ field: string, unit: BucketUnit, from: string | Date, to: string | Date, empty?: Record<string, any> }} opts
 * @returns {Array<G | { key: Record<string, any>, count: number, [metric: string]: any }>}
 */
export function fillBuckets(groups, opts) {
  const { field, unit, empty = {} } = opts
  if (!KEY[unit]) throw new TypeError(`backd: unit must be day, week, month or year, got ${JSON.stringify(unit)}`)
  const name = `${field}:${unit}`
  for (const g of groups) {
    const keys = Object.keys(g.key)
    if (keys.length !== 1 || keys[0] !== name) {
      throw new TypeError(`backd: fillBuckets needs groups grouped only by ${name}, got ${JSON.stringify(keys)}`)
    }
  }
  const from = toKey(opts.from, unit, 'from')
  const to = toKey(opts.to, unit, 'to')
  if (from > to) throw new TypeError(`backd: from (${from}) is after to (${to})`)
  const byKey = new Map(groups.map((g) => [g.key[name], g]))
  /** @type {any[]} */
  const out = []
  if (byKey.has(null)) out.push(byKey.get(null))
  const limit = 5000
  let cursor = keyStart(from, unit)
  for (let i = 0; ; i++) {
    const key = bucketKey(cursor, unit)
    if (key > to) break
    if (i >= limit) throw new RangeError(`backd: more than ${limit} buckets between ${from} and ${to}: use a larger unit`)
    out.push(byKey.get(key) ?? { key: { [name]: key }, count: 0, ...empty })
    cursor = next(unit, cursor)
  }
  return out
}
