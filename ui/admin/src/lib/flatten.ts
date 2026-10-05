// Settings come as nested objects; the settings page shows one row per leaf,
// with its dotted path, so a long configuration stays scannable and
// copy-able.

export interface Leaf {
  path: string
  value: string
}

export function flatten(value: unknown, prefix = ''): Leaf[] {
  if (Array.isArray(value)) {
    // A list of plain values is one row; a list of objects is one row per item.
    if (value.every((v) => v === null || typeof v !== 'object')) return [{ path: prefix, value: value.join(', ') }]
    return value.flatMap((v, i) => flatten(v, `${prefix}[${i}]`))
  }
  if (value !== null && typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
    if (!entries.length) return [{ path: prefix, value: '' }]
    return entries.flatMap(([k, v]) => flatten(v, prefix ? `${prefix}.${k}` : k))
  }
  return [{ path: prefix, value: value === null || value === undefined ? '' : String(value) }]
}
