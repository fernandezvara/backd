// Dates and numbers follow the browser's locale; nothing here is translated.

const date = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function formatDate(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : date.format(d)
}

/** The server's own words for what went wrong, or a fallback. */
export function errorText(e: unknown, fallback: string): string {
  return e instanceof Error && e.message ? e.message : fallback
}

/** "a, b ,,c" → ["a", "b", "c"]. */
export function parseList(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

/** Go durations as the server prints them ("720h0m0s", "1m0s") in the shortest readable form ("30d", "1m"). */
export function prettyDuration(value: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(value)
  if (!m || !value) return value
  const hours = Number(m[1] ?? 0)
  const minutes = Number(m[2] ?? 0)
  const seconds = Number(m[3] ?? 0)
  const parts: string[] = []
  if (hours >= 24) parts.push(`${Math.floor(hours / 24)}d`)
  if (hours % 24) parts.push(`${hours % 24}h`)
  if (minutes) parts.push(`${minutes}m`)
  if (seconds) parts.push(`${seconds}s`)
  return parts.length ? parts.join('') : '0s'
}
