// The money logic of the expenses example, kept apart from the UI so it
// can be tested. It runs in the browser, so it is only as trustworthy as
// the client running it (hole 4 in the docs): a server-side function will
// compute balances once functions exist.

/**
 * @typedef {object} Entry
 * @property {'expense' | 'settlement'} kind
 * @property {number} amount            Integer cents.
 * @property {string} paid_by
 * @property {string[]} split_between
 */

/**
 * Each person's net position in cents: positive when the group owes them,
 * negative when they owe the group. A split's remainder cents go to the
 * first people in split_between, so shares always add up to the amount.
 * A settlement moves its amount from the payer's debt to the receiver.
 * @param {Entry[]} entries
 * @returns {Record<string, number>}
 */
export function balances(entries) {
  /** @type {Record<string, number>} */
  const net = {}
  /** @param {string} who @param {number} cents */
  const add = (who, cents) => (net[who] = (net[who] ?? 0) + cents)
  for (const e of entries) {
    add(e.paid_by, e.amount)
    const n = e.split_between.length
    const share = Math.floor(e.amount / n)
    let rest = e.amount - share * n
    for (const who of e.split_between) {
      add(who, -(share + (rest > 0 ? 1 : 0)))
      rest--
    }
  }
  return net
}

/**
 * Pairs debtors with creditors, largest first, into a short list of
 * payments that settles everyone.
 * @param {Record<string, number>} net
 * @returns {{ from: string, to: string, amount: number }[]}
 */
export function settlementPlan(net) {
  const debtors = Object.entries(net).filter(([, v]) => v < 0).map(([who, v]) => ({ who, v: -v }))
  const creditors = Object.entries(net).filter(([, v]) => v > 0).map(([who, v]) => ({ who, v }))
  debtors.sort((a, b) => b.v - a.v || a.who.localeCompare(b.who))
  creditors.sort((a, b) => b.v - a.v || a.who.localeCompare(b.who))
  const plan = []
  let i = 0
  let j = 0
  while (i < debtors.length && j < creditors.length) {
    const amount = Math.min(debtors[i].v, creditors[j].v)
    plan.push({ from: debtors[i].who, to: creditors[j].who, amount })
    debtors[i].v -= amount
    creditors[j].v -= amount
    if (debtors[i].v === 0) i++
    if (creditors[j].v === 0) j++
  }
  return plan
}

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
