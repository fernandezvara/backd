/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * A consent the realm asks for, with the version in force.
 * @typedef {object} ConsentInfo
 * @property {string} name
 * @property {boolean} required     Needed for an account; optional ones are given and withdrawn at will.
 * @property {{ version: string, reaccept: boolean, reaccept_by?: string }} current  The version in force; `reaccept`
 *   says people who accepted an older one are asked again, `reaccept_by` (a date) when open sessions are refused.
 * @property {string[]} locales     The languages its text exists in.
 */

/**
 * The exact text of a version.
 * @typedef {object} ConsentText
 * @property {string} name
 * @property {string} version
 * @property {string} locale  The language of the text: the best the realm has for the one asked for.
 * @property {string} text    Markdown.
 * @property {string} hash    SHA-256 of the text: what a consent record keeps as proof.
 * @property {boolean} current  The version in force.
 */

/**
 * What the signed-in user holds and has still to accept.
 * @typedef {object} ConsentState
 * @property {Record<string, string>} consents   Consent name to the version the user holds.
 * @property {Array<{ name: string, version: string }>} to_accept  Required consents to accept (again).
 * @property {Array<{ consent: string, version: string, action: 'accepted' | 'withdrawn', method: string, locale: string, at: string }>} [history]
 *   With {@link Consents#state}, oldest first.
 */

/**
 * Consents the realm asks for: `client.consents`. The texts are public (a person reads them before
 * having an account); the rest needs a session. See {@link Auth#signup} and {@link Auth#acceptConsents}
 * for accepting at sign-up and at sign-in.
 */
export class Consents {
  /** @param {Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
  }

  /**
   * The consents the realm asks for and the version in force of each.
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConsentInfo[]>}
   */
  async current(opts) {
    const { data } = await this.client.request({ method: 'GET', path: ['_consents'], auth: false, ...opts })
    return data.items
  }

  /**
   * The exact text of a version, in the best of the realm's languages for `locale`.
   * @param {string} name
   * @param {string} version
   * @param {string} [locale]
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConsentText>}
   */
  async text(name, version, locale, opts) {
    const { data } = await this.client.request({
      method: 'GET', path: ['_consents', name, version], query: { locale }, auth: false, ...opts,
    })
    return data
  }

  /**
   * What the signed-in user holds, what they still have to accept and their history.
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConsentState>}
   */
  async state(opts) {
    return (await this.client.request({ method: 'GET', path: ['_auth', 'consents'], ...opts })).data
  }

  /**
   * The signed-in user accepts consents: `{ terms: '2026-10', marketing: '1' }`. It works for a user who is
   * refused with `consent_required` because a `reaccept_by` date passed, which is how they get back in.
   * @param {Record<string, string>} consents  Consent name to the version accepted.
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConsentState>}
   */
  async accept(consents, opts) {
    return (await this.client.request({ method: 'POST', path: ['_auth', 'consents'], body: { consents }, ...opts })).data
  }

  /**
   * Withdraws an optional consent. A required one is a `ConflictError` with the code
   * `consent_required_for_account`: the way out is deleting the account.
   * @param {string} name
   * @param {RequestOptions} [opts]
   * @returns {Promise<ConsentState>}
   */
  async withdraw(name, opts) {
    return (await this.client.request({ method: 'DELETE', path: ['_auth', 'consents', name], ...opts })).data
  }
}
