// Reference expansion for the fake store: the same syntax, checks and answers as `?expand=` on
// backd (name(field,field), name.nested, `_expanded`, null for missing or unreadable, `truncated`),
// over what a test declared with `store.declareReferences` and `store.declareReadRule`.
import { ExpandTooLargeError, InvalidExpandError } from '../../js/src/errors.js'

const NAME = /^[A-Za-z][A-Za-z0-9_]*$/
const FIELD = /^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$/

/** What backd does by default; a test can lower them with `createContext({ expand: {...} })`. */
export const DEFAULT_LIMITS = { maxDepth: 2, maxPaths: 5, maxArray: 100, maxDocs: 2000 }

/**
 * @param {string} message
 * @returns {InvalidExpandError}
 */
function invalid(message) {
  return new InvalidExpandError({ status: 400, code: 'invalid_expand', message: `invalid expand: ${message}`, details: [{ path: 'expand', reason: message }] })
}

/**
 * @param {string} s
 * @returns {{ names: string[], fields: string[] | null, text: string }[]}
 */
export function parseExpand(s) {
  if (s === undefined || s === null || String(s).trim() === '') return []
  const entries = []
  let depth = 0
  let start = 0
  for (let i = 0; i <= s.length; i++) {
    const c = s[i]
    if (c === '(') depth++
    else if (c === ')') depth--
    if (depth < 0) throw invalid(`unbalanced parentheses in ${s}`)
    if (i === s.length || (c === ',' && depth === 0)) {
      entries.push(s.slice(start, i).trim())
      start = i + 1
    }
  }
  if (depth !== 0) throw invalid(`unbalanced parentheses in ${s}`)
  return entries.map((text) => {
    if (text === '') throw invalid('(empty): an empty entry: remove the extra comma')
    const open = text.indexOf('(')
    const head = open === -1 ? text : text.slice(0, open)
    let fields = null
    if (open !== -1) {
      const tail = text.slice(open + 1)
      if (!tail.endsWith(')') || /[()]/.test(tail.slice(0, -1))) throw invalid(`${text}: fields go in one pair of parentheses at the end: name(field,field)`)
      fields = tail.slice(0, -1).split(',').map((f) => f.trim())
      for (const f of fields) if (!FIELD.test(f)) throw invalid(`${text}: "${f}" is not a field name`)
    }
    const names = head.trim().split('.')
    for (const n of names) if (!NAME.test(n)) throw invalid(`${text}: "${n}" is not a reference name`)
    return { names, fields, text }
  })
}

/**
 * What a store holds that expansion needs.
 * @typedef {object} ExpandSource
 * @property {(database: string, name: string) => Map<string, any>} collection
 * @property {(database: string, name: string) => Record<string, Reference> | undefined} referencesOf
 * @property {(database: string, name: string) => ((doc: any, who: { user: any }) => boolean) | undefined} readRuleOf
 */

/**
 * @typedef {object} Reference
 * @property {string} name
 * @property {string} field
 * @property {string} database
 * @property {string} collection
 * @property {string} key
 * @property {boolean} many
 * @property {number} [maxExpand]
 */

/**
 * Normalizes what `declareReferences` is given, with the same defaults as collection.yaml.
 * @param {string} database
 * @param {Record<string, any>} refs
 * @returns {Record<string, Reference>}
 */
export function normalizeReferences(database, refs) {
  /** @type {Record<string, Reference>} */
  const out = {}
  for (const [name, r] of Object.entries(refs)) {
    if (!NAME.test(name)) throw new TypeError(`@backd/functions-testing: "${name}" is not a reference name`)
    if (!r || typeof r.collection !== 'string' || r.collection === '') throw new TypeError(`@backd/functions-testing: references.${name}.collection is required`)
    if (r.max_expand !== undefined && (!r.many || !Number.isInteger(r.max_expand) || r.max_expand < 1)) {
      throw new TypeError(`@backd/functions-testing: references.${name}.max_expand needs many: true and a positive integer`)
    }
    out[name] = { name, field: r.field ?? name, database: r.database ?? database, collection: r.collection, key: r.key ?? 'id', many: !!r.many, maxExpand: r.max_expand }
  }
  return out
}

function valueAt(doc, path) {
  let cur = doc
  for (const p of path.split('.')) {
    if (cur === null || typeof cur !== 'object') return undefined
    cur = cur[p]
  }
  return cur
}

function select(doc, fields) {
  if (!fields) return structuredClone(doc)
  const out = { id: doc.id }
  for (const f of fields) {
    const v = valueAt(doc, f)
    if (v === undefined) continue
    const parts = f.split('.')
    let m = out
    for (const p of parts.slice(0, -1)) m = m[p] ??= {}
    m[parts.at(-1)] = structuredClone(v)
  }
  return out
}

/**
 * Checks `expand` against the collection's declared references.
 * @param {ExpandSource} src
 * @param {string} database
 * @param {string} name
 * @param {string} expand
 * @param {typeof DEFAULT_LIMITS} limits
 */
export function planExpand(src, database, name, expand, limits) {
  const entries = parseExpand(expand)
  if (entries.length > limits.maxPaths) throw invalid(`${entries.length} entries, at most ${limits.maxPaths} per request`)
  /** @type {any[]} */
  const roots = []
  for (const e of entries) {
    if (e.names.length > limits.maxDepth) throw invalid(`${e.text}: goes ${e.names.length} levels deep, at most ${limits.maxDepth} are followed`)
    let level = roots
    let from = [database, name]
    let node
    e.names.forEach((n, i) => {
      const refs = src.referencesOf(from[0], from[1]) ?? {}
      const ref = refs[n]
      if (!ref) {
        const declared = Object.keys(refs).sort()
        throw invalid(`${e.text}: "${n}" is not a reference of ${from[1]}; ${declared.length ? 'declared: ' + declared.join(', ') : from[1] + ' declares no references'}`)
      }
      node = level.find((x) => x.ref.name === n)
      if (!node) {
        node = { ref, target: [ref.database, ref.collection], fields: null, explicit: false, children: [] }
        level.push(node)
      }
      if (i === e.names.length - 1) {
        if (node.explicit) throw invalid(`${e.text}: "${n}" is listed twice`)
        node.explicit = true
        node.fields = e.fields
      }
      level = node.children
      from = node.target
    })
  }
  return roots
}

/**
 * Adds `_expanded` to copies of docs.
 * @param {ExpandSource} src
 * @param {any[]} docs
 * @param {any[]} plan
 * @param {{ user: any, admin: boolean }} who
 * @param {typeof DEFAULT_LIMITS} limits
 * @returns {any[]}
 */
export function expandDocs(src, docs, plan, who, limits) {
  const budget = { docs: 0 }
  const out = docs.map((d) => ({ ...d }))
  follow(src, docs, out, plan, who, limits, budget)
  return out
}

function follow(src, parents, outs, nodes, who, limits, budget) {
  for (const n of nodes) {
    const maxArray = Math.min(limits.maxArray, n.ref.maxExpand ?? limits.maxArray)
    const perParent = parents.map((p) => {
      const v = valueAt(p, n.ref.field)
      const vals = (n.ref.many ? (Array.isArray(v) ? v : []) : [v]).filter((x) => typeof x === 'string' && x !== '')
      return [...new Set(vals)]
    })
    const truncated = perParent.map((v) => v.length > maxArray)
    perParent.forEach((v, i) => {
      if (truncated[i]) perParent[i] = v.slice(0, maxArray)
    })
    const total = perParent.reduce((a, v) => a + v.length, 0)
    if (budget.docs + total > limits.maxDocs) {
      throw new ExpandTooLargeError({ status: 400, code: 'expand_too_large', message: `this expansion would return ${budget.docs + total} documents, at most ${limits.maxDocs} per response` })
    }
    const [tdb, tname] = n.target
    const rule = who.admin ? undefined : src.readRuleOf(tdb, tname)
    const readable = (d) => !rule || rule(d, { user: who.user })
    const targets = [...src.collection(tdb, tname).values()]
    const byKey = new Map()
    for (const d of targets) {
      const k = n.ref.key === 'id' ? d.id : valueAt(d, n.ref.key)
      if (typeof k === 'string' && !byKey.has(k) && readable(d)) byKey.set(k, d)
    }
    const shaped = new Map()
    for (const [k, d] of byKey) shaped.set(k, select(d, n.fields))
    outs.forEach((o, i) => {
      const vals = perParent[i]
      let v
      if (n.ref.many) {
        const items = vals.map((k) => (shaped.has(k) ? shaped.get(k) : null))
        budget.docs += items.filter((x) => x !== null).length
        v = truncated[i] ? { items, truncated: true } : items
      } else if (vals.length === 0) {
        v = null
      } else {
        v = shaped.get(vals[0]) ?? null
        if (v !== null) budget.docs++
      }
      o._expanded = { ...(o._expanded ?? {}), [n.ref.name]: v }
    })
    if (n.children.length > 0) {
      const keys = [...shaped.keys()]
      follow(src, keys.map((k) => byKey.get(k)), keys.map((k) => shaped.get(k)), n.children, who, limits, budget)
    }
  }
}
