// Realm names as backd accepts them in URLs.
export const REALM_PATTERN = /^[a-z0-9][a-z0-9_-]{0,62}$/

const KEY = 'backd-admin-realm'

/** The realm of the last sign-in, to pre-fill the form; a name, never a secret. */
export function lastRealm(): string {
  try {
    const r = localStorage.getItem(KEY) ?? ''
    return validRealm(r) ? r : ''
  } catch {
    return ''
  }
}

export function rememberRealm(name: string) {
  try {
    localStorage.setItem(KEY, name)
  } catch {
    // storage refused: the form is just empty next time
  }
}

export function validRealm(name: string): boolean {
  return REALM_PATTERN.test(name)
}
