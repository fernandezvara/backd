// Aggregation and text search for the fake store: the same parameters, groups and result names as
// backd, over documents held in memory. Aggregation follows backd's semantics (null groups, exact
// decimal sums as strings, buckets in a time zone); search is an approximation, said so in the README.
import { AggregateNotEnabledError, InvalidAggregateError } from '../../js/src/errors.js'
import { bucketKey } from '../../js/src/buckets.js'

const METRICS = ['count', 'count_distinct', 'sum', 'avg', 'min', 'max']
const BUCKETS = ['day', 'week', 'month', 'year']

function get(doc, path) {
  return path.split('.').reduce((v, k) => (v === null || v === undefined ? undefined : v[k]), doc)
}

function invalid(path, reason) {
  return new InvalidAggregateError({ status: 400, code: 'invalid_aggregate', message: 'invalid aggregate parameters', details: [{ path, reason }] })
}

/** The wall-clock parts of an instant in an IANA zone, as a Date whose UTC fields hold them. */
function inZone(instant, tz) {
  if (!tz || tz === 'UTC') return instant
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).formatToParts(instant)
  const p = Object.fromEntries(parts.map((x) => [x.type, x.value]))
  return new Date(Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second))
}

/** Splits a decimal text into a BigInt and its number of fraction digits. */
function scaled(n) {
  const s = typeof n === 'number' ? n.toLocaleString('en-US', { useGrouping: false, maximumFractionDigits: 20 }) : String(n)
  const [int, frac = ''] = s.split('.')
  return { v: BigInt(int + frac), scale: frac.length }
}

function decimalText(v, scale) {
  const neg = v < 0n
  let s = (neg ? -v : v).toString().padStart(scale + 1, '0')
  if (scale > 0) s = `${s.slice(0, -scale)}.${s.slice(-scale)}`
  s = s.includes('.') ? s.replace(/0+$/, '').replace(/\.$/, '') : s
  return (neg ? '-' : '') + s
}

/** Sum or average of numbers as an exact decimal string, `null` for the average of nothing. */
function decimal(values, avg) {
  const nums = values.filter((x) => typeof x === 'number')
  if (avg && nums.length === 0) return null
  const parts = nums.map(scaled)
  const scale = Math.max(0, ...parts.map((p) => p.scale))
  const total = parts.reduce((a, p) => a + p.v * 10n ** BigInt(scale - p.scale), 0n)
  if (!avg) return decimalText(total, scale)
  // Divide with 34 significant digits at most, like the server's decimal type.
  const n = BigInt(nums.length)
  const extra = 34
  const q = (total * 10n ** BigInt(extra)) / n
  const text = decimalText(q, scale + extra)
  const [int, frac = ''] = text.split('.')
  const digits = int.replace('-', '').length
  return frac === '' ? int : `${int}.${frac.slice(0, Math.max(0, 34 - digits))}`.replace(/0+$/, '').replace(/\.$/, '')
}

function compare(a, b) {
  if (a === b) return 0
  if (a === null || a === undefined) return -1
  if (b === null || b === undefined) return 1
  if (typeof a === 'string' && typeof b === 'string' && /^-?\d+(\.\d+)?$/.test(a) && /^-?\d+(\.\d+)?$/.test(b)) return Number(a) < Number(b) ? -1 : 1
  return a < b ? -1 : 1
}

/**
 * @param {any[]} docs Documents already restricted by where.
 * @param {Record<string, any>} params
 * @param {{ group_by?: string[], metrics?: string[] } | undefined} declared What the collection declares, if the test did.
 */
export function aggregate(docs, params = {}, declared) {
  if (declared === null) {
    throw new AggregateNotEnabledError({ status: 400, code: 'aggregate_not_enabled', message: 'this collection declares no aggregate:' })
  }
  const list = (v) => (v === undefined ? [] : Array.isArray(v) ? v : String(v).split(','))
  const groupBy = list(params.groupBy).map((g) => {
    const [field, bucket] = String(g).split(':')
    if (bucket !== undefined && !BUCKETS.includes(bucket)) throw invalid('group_by', `${JSON.stringify(bucket)} is not a bucket: use day, week, month or year`)
    if (declared?.group_by && !declared.group_by.includes(field)) throw invalid('group_by', `${JSON.stringify(field)} is not a field this collection declares in aggregate.group_by`)
    return { field, bucket, name: bucket ? `${field}:${bucket}` : field }
  })
  if (groupBy.length > 3) throw invalid('group_by', `${groupBy.length} fields, at most 3`)
  const metrics = (list(params.metrics).length ? list(params.metrics) : ['count']).map((m) => {
    const [op, field] = String(m).split(':')
    if (!METRICS.includes(op)) throw invalid('metrics', `${JSON.stringify(op)} is not a metric`)
    if (op !== 'count' && !field) throw invalid('metrics', `${op} needs a field: ${op}:<field>`)
    if (declared?.metrics && ['sum', 'avg', 'min', 'max'].includes(op) && !declared.metrics.includes(field)) {
      throw invalid('metrics', `${op}:${field} needs a field of aggregate.metrics`)
    }
    return { op, field, name: op === 'count' ? 'count' : `${op}_${field}` }
  })
  const tz = params.tz

  const buckets = new Map()
  for (const doc of docs) {
    const key = groupBy.map((g) => {
      const v = get(doc, g.field)
      if (!g.bucket) return v === undefined ? null : v
      const d = v instanceof Date ? v : typeof v === 'string' ? new Date(v) : null
      return d && !Number.isNaN(d.getTime()) ? bucketKey(inZone(d, tz), g.bucket) : null
    })
    const id = JSON.stringify(key)
    if (!buckets.has(id)) buckets.set(id, { key, docs: [] })
    buckets.get(id).docs.push(doc)
  }
  if (groupBy.length === 0 && buckets.size === 0) buckets.set('[]', { key: [], docs: [] })

  let groups = [...buckets.values()].map(({ key, docs: members }) => {
    const out = { key: Object.fromEntries(groupBy.map((g, i) => [g.name, key[i]])) }
    for (const m of metrics) {
      const vals = m.field ? members.map((d) => get(d, m.field)) : []
      switch (m.op) {
        case 'count':
          out.count = members.length
          break
        case 'count_distinct':
          out[m.name] = new Set(vals.filter((v) => v !== null && v !== undefined).map((v) => JSON.stringify(v))).size
          break
        case 'sum':
          out[m.name] = decimal(vals, false)
          break
        case 'avg':
          out[m.name] = decimal(vals, true)
          break
        default: {
          const present = vals.filter((v) => v !== null && v !== undefined)
          out[m.name] = present.length === 0 ? null : present.reduce((a, b) => ((m.op === 'min') === (compare(b, a) < 0) ? b : a))
        }
      }
    }
    return out
  })
  const names = groupBy.map((g) => g.name)
  const order = list(params.orderBy).map((o) => String(o))
  groups.sort((a, b) => {
    for (const o of order) {
      const desc = o.startsWith('-')
      const name = desc ? o.slice(1) : o
      const av = name in a.key ? a.key[name] : a[name]
      const bv = name in b.key ? b.key[name] : b[name]
      const c = compare(av, bv)
      if (c !== 0) return desc ? -c : c
    }
    for (const n of names) {
      const c = compare(a.key[n], b.key[n])
      if (c !== 0) return c
    }
    return 0
  })
  const total = groups.length
  groups = groups.slice(0, params.limit ?? 100)
  return { groups, total_groups: total }
}

/** Words, "quoted phrases" and -excluded words of a search text, lowercased. */
function tokens(text) {
  const out = []
  const re = /(-?)"([^"]*)"|(-?)(\S+)/g
  let m
  while ((m = re.exec(text)) !== null) {
    const exclude = (m[1] || m[3]) === '-'
    const t = (m[2] ?? m[4]).toLowerCase()
    if (t) out.push({ t, exclude })
  }
  return out
}

/**
 * Approximates a text search: a document matches when any word or phrase appears in an indexed
 * field (case-insensitively, no stemming) and no excluded word does. The score is the sum of the
 * weights of the terms found. Returns the matching documents with `_meta.score`, best first.
 * @param {any[]} docs
 * @param {string} text
 * @param {Record<string, number> | undefined} weights  The declared text fields; every string field when absent.
 */
export function search(docs, text, weights) {
  const terms = tokens(text)
  const scored = []
  for (const doc of docs) {
    const fields = weights ?? Object.fromEntries(Object.keys(doc).filter((k) => k !== '_meta' && k !== 'id').map((k) => [k, 1]))
    const bag = Object.entries(fields).map(([f, w]) => ({ w, text: [].concat(get(doc, f) ?? []).filter((x) => typeof x === 'string').join(' ').toLowerCase() }))
    let score = 0
    let rejected = false
    for (const { t, exclude } of terms) {
      const hit = bag.filter((b) => b.text.includes(t))
      if (exclude) rejected ||= hit.length > 0
      else score += hit.reduce((a, b) => a + b.w, 0)
    }
    if (!rejected && score > 0) scored.push({ doc: { ...doc, _meta: { ...doc._meta, score } }, score })
  }
  return scored.sort((a, b) => b.score - a.score || (a.doc.id < b.doc.id ? -1 : 1)).map((s) => s.doc)
}
