/**
 * Where the client keeps the session token. Methods may be async.
 * @typedef {object} TokenStorage
 * @property {() => string | null | undefined | Promise<string | null | undefined>} get
 * @property {(token: string) => void | Promise<void>} set
 * @property {() => void | Promise<void>} remove
 */

/**
 * Keeps the token in memory: it is lost on reload, and scripts on the page
 * can't read it from storage. The default.
 * @returns {TokenStorage}
 */
export function memoryStorage() {
  /** @type {string | null} */
  let token = null
  return {
    get: () => token,
    set: (t) => {
      token = t
    },
    remove: () => {
      token = null
    },
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
  }
}
