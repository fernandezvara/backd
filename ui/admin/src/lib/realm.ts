// Realm names as backd accepts them in URLs.
export const REALM_PATTERN = /^[a-z0-9][a-z0-9_-]{0,62}$/

export function validRealm(name: string): boolean {
  return REALM_PATTERN.test(name)
}
