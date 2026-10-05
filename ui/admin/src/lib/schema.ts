// The part of JSON Schema backd's collections use, as far as a form can
// render it. Anything else falls back to a JSON editor for that field (or
// the whole document): the form never guesses.

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Schema = Record<string, any>

export type Kind = 'object' | 'array' | 'string' | 'number' | 'integer' | 'boolean' | 'enum' | 'json'

export interface Resolved {
  kind: Kind
  schema: Schema
  /** `type: ["string", "null"]`: the field may hold null. */
  nullable: boolean
  /** Why a field is a JSON editor, when it is one. */
  reason?: string
}

/** Keywords a form can't render faithfully. */
const UNSUPPORTED = ['oneOf', 'anyOf', 'allOf', 'not', 'if', 'then', 'else', 'patternProperties', 'dependentSchemas', 'dependentRequired', 'dependencies', 'propertyNames', 'prefixItems', 'contains', 'unevaluatedProperties', 'unevaluatedItems']

function pointer(root: Schema, ref: string): Schema | undefined {
  if (!ref.startsWith('#')) return undefined
  let node: unknown = root
  for (const raw of ref.slice(1).split('/').filter(Boolean)) {
    const key = decodeURIComponent(raw).replace(/~1/g, '/').replace(/~0/g, '~')
    if (node === null || typeof node !== 'object') return undefined
    node = (node as Record<string, unknown>)[key]
  }
  return node !== null && typeof node === 'object' ? (node as Schema) : undefined
}

/**
 * Follows local `$ref`s. A reference met again while resolving itself is
 * recursive: a form can't be drawn from it, so it is reported instead of
 * followed.
 */
export function deref(schema: Schema, root: Schema, seen: string[] = []): { schema: Schema; reason?: string } {
  let current = schema
  const path = [...seen]
  while (typeof current.$ref === 'string') {
    const ref = current.$ref as string
    if (path.includes(ref)) return { schema: current, reason: 'recursive $ref' }
    const target = pointer(root, ref)
    if (!target) return { schema: current, reason: 'unresolved $ref' }
    path.push(ref)
    const { $ref: _ref, ...siblings } = current
    void _ref
    current = { ...target, ...siblings }
  }
  return { schema: current }
}

/** What kind of control a schema gets. */
export function resolve(schema: Schema | boolean | undefined, root: Schema, seen: string[] = []): Resolved {
  if (schema === undefined || typeof schema === 'boolean') return { kind: 'json', schema: {}, nullable: false, reason: 'no type' }
  const { schema: s, reason } = deref(schema, root, seen)
  if (reason) return { kind: 'json', schema: s, nullable: false, reason }
  const bad = UNSUPPORTED.find((k) => k in s)
  if (bad) return { kind: 'json', schema: s, nullable: false, reason: bad }

  let nullable = false
  let type = s.type as string | string[] | undefined
  if (Array.isArray(type)) {
    const rest = type.filter((t) => t !== 'null')
    nullable = rest.length !== type.length
    if (rest.length !== 1) return { kind: 'json', schema: s, nullable, reason: 'type list' }
    type = rest[0]
  }

  if (Array.isArray(s.enum) || 'const' in s) {
    const options = Array.isArray(s.enum) ? s.enum : [s.const]
    if (options.every((o: unknown) => o === null || ['string', 'number', 'boolean'].includes(typeof o))) {
      return { kind: 'enum', schema: { ...s, enum: options }, nullable: nullable || options.includes(null) }
    }
    return { kind: 'json', schema: s, nullable, reason: 'enum of objects' }
  }

  switch (type) {
    case 'object':
      return s.properties && typeof s.properties === 'object' ? { kind: 'object', schema: s, nullable } : { kind: 'json', schema: s, nullable, reason: 'free-form object' }
    case 'array':
      return s.items && typeof s.items === 'object' && !Array.isArray(s.items) ? { kind: 'array', schema: s, nullable } : { kind: 'json', schema: s, nullable, reason: 'array without items' }
    case 'string':
    case 'number':
    case 'integer':
    case 'boolean':
      return { kind: type, schema: s, nullable }
    case undefined:
      return s.properties ? { kind: 'object', schema: s, nullable } : { kind: 'json', schema: s, nullable, reason: 'no type' }
    default:
      return { kind: 'json', schema: s, nullable, reason: 'type' }
  }
}

/** Whether the whole document can be edited as a form: the root is an object with declared properties. */
export function formable(root: Schema | undefined): boolean {
  return !!root && resolve(root, root).kind === 'object'
}

/** The value a new field of this kind starts with; undefined: left empty. */
export function initialValue(schema: Schema, root: Schema, seen: string[] = []): unknown {
  const r = resolve(schema, root, seen)
  if (r.schema.default !== undefined) return structuredClone(r.schema.default)
  switch (r.kind) {
    case 'object': {
      const out: Record<string, unknown> = {}
      const required: string[] = Array.isArray(r.schema.required) ? r.schema.required : []
      for (const key of required) {
        const prop = (r.schema.properties as Record<string, Schema>)[key]
        if (!prop) continue
        const v = initialValue(prop, root, seen)
        if (v !== undefined) out[key] = v
      }
      return out
    }
    case 'array':
      return []
    case 'string':
      return ''
    case 'boolean':
      return false
    default:
      return undefined
  }
}

/** A new document: only the required fields, with their starting values. */
export function newDocument(root: Schema): Record<string, unknown> {
  return (initialValue(root, root) as Record<string, unknown>) ?? {}
}

/** `2026-10-05T07:30:00.000Z` → the value of a datetime-local input, in the browser's zone. */
export function isoToLocalInput(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** The value of a datetime-local input → an RFC 3339 instant. */
export function localInputToIso(local: string): string {
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

/** Server validation details by dot path, so each lands next to its field. */
export function errorsByPath(details: { path: string; reason: string }[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const d of details) out[d.path] = out[d.path] ? `${out[d.path]}; ${d.reason}` : d.reason
  return out
}

/** Join a path: ("author", "name") → "author.name"; array items use their index ("tags.0"). */
export const join = (base: string, key: string | number) => (base ? `${base}.${key}` : String(key))
