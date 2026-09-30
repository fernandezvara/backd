// Unlike ../expenses-without-functions/ledger.js, there's almost nothing
// left here: balances and the settlement plan are computed by the
// balances function now (hole 4), not the browser. The only money math
// still in the client is turning what someone typed into integer cents
// before sending it to the server, which is authoritative about
// everything past that point.

/**
 * Parses an amount typed by a person ("12.5", "12,50") into cents; 0 for
 * anything that isn't a positive number.
 * @param {string | number} value
 * @returns {number}
 */
export function toCents(value) {
  const n = Number(String(value).trim().replace(',', '.'))
  return Number.isFinite(n) && n > 0 ? Math.round(n * 100) : 0
}
