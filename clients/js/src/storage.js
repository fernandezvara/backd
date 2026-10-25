/**
 * Where the client keeps the session token. Methods may be async.
 *
 * A storage that can also hold one token per organization (`slot`) lets one app be signed in to
 * several organizations at once, with `client.org(id)`. `slot(name)` is an independent storage
 * under a name; `slots()` lists the names that hold a token. Both built-in storages have them.
 * @typedef {object} TokenStorage
 * @property {() => string | null | undefined | Promise<string | null | undefined>} get
 * @property {(token: string) => void | Promise<void>} set
 * @property {() => void | Promise<void>} remove
 * @property {(name: string) => TokenStorage} [slot]
 * @property {() => string[] | Promise<string[]>} [slots]
 */

/**
 * What the client stores, instead of a token, when the session lives in an
 * HttpOnly cookie (`cookies: true`): a hint that there is a session, with
 * nothing secret in it.
 */
export const COOKIE_SESSION = 'cookie-session'

/**
 * Keeps the token in memory: it is lost on reload, and scripts on the page
 * can't read it from storage. The default.
 * @returns {TokenStorage}
 */
export function memoryStorage() {
  /** @type {string | null} */
  let token = null
  /** @type {Map<string, TokenStorage>} */
  const slots = new Map()
  return {
    get: () => token,
    set: (t) => {
      token = t
    },
    remove: () => {
      token = null
    },
    slot: (name) => {
      let s = slots.get(name)
      if (!s) slots.set(name, (s = memoryStorage()))
      return s
    },
    slots: () => [...slots].filter(([, s]) => s.get()).map(([name]) => name),
  }
}

/**
 * Keeps the token in `localStorage`, so sessions survive reloads. Any
 * script running on the page (including injected ones) can read it:
 * protect the app against cross-site scripting.
 * @param {string} [key] Storage key; defaults to "backd.session".
 * @returns {TokenStorage}
 */
export function localStorageStorage(key = 'backd.session') {
  const ls = globalThis.localStorage
  if (!ls) throw new Error('localStorage is not available in this runtime')
  return {
    get: () => ls.getItem(key),
    set: (t) => ls.setItem(key, t),
    remove: () => ls.removeItem(key),
    slot: (name) => localStorageStorage(key + ':' + name),
    slots: () => {
      const names = []
      for (let i = 0; i < ls.length; i++) {
        const k = ls.key(i)
        if (k && k.startsWith(key + ':')) names.push(k.slice(key.length + 1))
      }
      return names
    },
  }
}
