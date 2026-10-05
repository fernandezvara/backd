/**
 * Where to go after signing in: the page asked for, only if it is inside
 * this realm's part of the app. Anything else (another realm, an address
 * with a scheme or a host, a path that climbs out) goes to the realm's home.
 */
export function safeNext(next: unknown, realm: string): string {
  const home = `/r/${realm}`
  if (typeof next !== 'string' || next.includes('\\') || /[\u0000-\u001f]/.test(next)) return home
  const inRealm = next === home || next.startsWith(`${home}/`) || next.startsWith(`${home}?`) || next.startsWith(`${home}#`)
  if (!inRealm) return home
  // No dot segments: /r/acme/../../x
  const path = next.split(/[?#]/, 1)[0]
  return path.split('/').some((seg) => seg === '.' || seg === '..') ? home : next
}
