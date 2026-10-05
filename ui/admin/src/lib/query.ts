// The query builder: the fields a collection's schema declares, the
// operators each type allows, and the `where` they make. Only what backd's
// query language accepts is offered, so the server has little to refuse.

import { resolve, type Schema } from './schema'

export type QueryType = 'string' | 'number' | 'integer' | 'boolean' | 'date' | 'enum'

export interface QueryField {
  path: string
  type: QueryType
  options?: unknown[]
}

const MAX_DEPTH = 4

/** Declared fields (nested ones by dot path) and the fields every document has. */
export function queryFields(root: Schema | undefined): QueryField[] {
  const out: QueryField[] = []
  const walk = (schema: Schema, prefix: string, depth: number, refs: string[]) => {
    const r = resolve(schema, root ?? {}, refs)
    const next = typeof schema.$ref === 'string' ? [...refs, schema.$ref as string] : refs
    switch (r.kind) {
      case 'object':
        if (depth >= MAX_DEPTH) return
        for (const [key, prop] of Object.entries(r.schema.properties as Record<string, Schema>)) walk(prop, prefix ? `${prefix}.${key}` : key, depth + 1, next)
        return
      case 'array':
        // A scalar array is queried like its items ("any element matches"); objects in arrays by dot path.
        walk(r.schema.items as Schema, prefix, depth, next)
        return
      case 'string':
        if (!prefix) return
        out.push({ path: prefix, type: r.schema['x-backd-store'] === 'date' ? 'date' : 'string' })
        return
      case 'number':
      case 'integer':
      case 'boolean':
        if (prefix) out.push({ path: prefix, type: r.kind })
        return
      case 'enum':
        if (prefix) out.push({ path: prefix, type: 'enum', options: r.schema.enum as unknown[] })
        return
      default:
        return // JSON-only fields are not queryable from the builder
    }
  }
  if (root) walk(root, '', 0, [])
  const seen = new Set<string>()
  const declared = out.filter((f) => (seen.has(f.path) ? false : (seen.add(f.path), true)))
  return [
    { path: 'id', type: 'string' },
    ...declared,
    { path: '_meta.created_at', type: 'date' },
    { path: '_meta.updated_at', type: 'date' },
    { path: '_meta.version', type: 'integer' },
    { path: '_meta.owner', type: 'string' },
    { path: '_meta.created_by', type: 'string' },
    { path: '_meta.updated_by', type: 'string' },
  ]
}

const TEXT = ['$icontains', '$contains', '$istartsWith', '$startsWith', '$iendsWith', '$endsWith', '$ilike', '$like']
const COMPARE = ['$gt', '$gte', '$lt', '$lte', '$between']
const NULLS = ['$isNull', '$notNull']

/** The operators the query language accepts on a type (text operators are refused on stored dates). */
export function operatorsFor(type: QueryType): string[] {
  switch (type) {
    case 'string':
      return ['$eq', '$ne', ...TEXT, ...COMPARE, '$in', '$nin', ...NULLS]
    case 'date':
      return ['$eq', '$ne', ...COMPARE, ...NULLS]
    case 'number':
    case 'integer':
      return ['$eq', '$ne', ...COMPARE, '$in', '$nin', ...NULLS]
    case 'boolean':
      return ['$eq', '$ne', ...NULLS]
    case 'enum':
      return ['$eq', '$ne', '$in', '$nin', ...NULLS]
  }
}

export interface Condition {
  field: string
  op: string
  /** What was typed: for $in/$nin a comma list, for $between "min, max", for $isNull/$notNull "true" or "false". */
  value: string
}

export class QueryError extends Error {}

function scalar(type: QueryType, text: string): unknown {
  const t = text.trim()
  switch (type) {
    case 'number': {
      const n = Number(t)
      if (t === '' || Number.isNaN(n)) throw new QueryError(`"${text}" is not a number`)
      return n
    }
    case 'integer': {
      const n = Number(t)
      if (t === '' || !Number.isInteger(n)) throw new QueryError(`"${text}" is not an integer`)
      return n
    }
    case 'boolean':
      if (t !== 'true' && t !== 'false') throw new QueryError(`"${text}" is not true or false`)
      return t === 'true'
    case 'date': {
      // A datetime-local value (what the date picker gives) or an RFC 3339 instant.
      const d = new Date(t)
      if (t === '' || Number.isNaN(d.getTime())) throw new QueryError(`"${text}" is not a date`)
      return d.toISOString()
    }
    default:
      return text
  }
}

function list(type: QueryType, text: string): unknown[] {
  const items = text.split(',').map((s) => s.trim()).filter((s) => s !== '')
  if (!items.length) throw new QueryError('give at least one value')
  return items.map((s) => scalar(type, s))
}

/** The `where` of a list of conditions, ANDed; the first one that can't be built is reported. */
export function buildWhere(conditions: Condition[], fields: QueryField[]): Record<string, Record<string, unknown>> {
  const where: Record<string, Record<string, unknown>> = {}
  for (const c of conditions) {
    const field = fields.find((f) => f.path === c.field)
    if (!field) throw new QueryError(`unknown field ${c.field}`)
    if (!operatorsFor(field.type).includes(c.op)) throw new QueryError(`${c.op} can't be used on ${c.field}`)
    let value: unknown
    if (c.op === '$isNull' || c.op === '$notNull') value = c.value !== 'false'
    else if (c.op === '$in' || c.op === '$nin') value = list(field.type, c.value)
    else if (c.op === '$between') {
      const [a, b, ...rest] = c.value.split(',')
      if (b === undefined || rest.length) throw new QueryError('$between takes two values: min, max')
      value = [scalar(field.type, a), scalar(field.type, b)]
    } else value = scalar(field.type, c.value)
    ;(where[c.field] ??= {})[c.op] = value
  }
  return where
}

/** `order_by` for a field and direction. */
export const orderBy = (field: string, dir: 'asc' | 'desc') => (field ? (dir === 'desc' ? `-${field}` : field) : undefined)
