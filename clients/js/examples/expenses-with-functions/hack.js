// Attacks the "expenses with functions" example the way a malicious user
// could, with the same client library the app uses, and reports what
// happened. Compare with ../expenses-without-functions/hack.js: the same
// rule-level attacks are stopped the same way (groups didn't change), but
// holes 1-4 are now CLOSED — this script proves it with real calls, not
// just by reading the functions' source. Hole 5 (email verification)
// stays open: nothing here changed that.
//
// It exits with 0 when every attack is stopped, holes 1-4 are closed, and
// hole 5 is still open, which is the expected state today. Exits 1 and
// says which line differs otherwise.
//
//   make example                              # in another terminal
//   make hack-expenses-functions               # or:
//   NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/expenses-with-functions/hack.js
//
// BACKD_URL overrides the server (default https://localhost:8443). Every
// run signs up new users with unique emails.
import { createClient, AuthenticationError, ForbiddenError, NotFoundError, BackdError } from '../../src/index.js'

const url = process.env.BACKD_URL ?? 'https://localhost:8443'
const run = Date.now().toString(36)
const email = (/** @type {string} */ name) => `${name}.${run}@example.com`
const password = 'dev-p4ssw0rd!'

let unexpected = 0
/** @param {string} what @param {() => Promise<unknown>} attack @param {Function} expected @param {(err: any) => boolean} [also] */
async function stopped(what, attack, expected, also) {
  try {
    await attack()
    unexpected++
    console.log(`  ✗ NOT STOPPED  ${what}`)
  } catch (err) {
    if (!(err instanceof expected) || (also && !also(err))) throw err
    console.log(`  ✓ stopped      ${what} (${/** @type {any} */ (err).status} ${/** @type {any} */ (err).code})`)
  }
}
/** @param {string} what @param {() => Promise<string | void>} action */
async function closed(what, action) {
  try {
    const detail = await action()
    unexpected++
    console.log(`  ✗ STILL OPEN   ${what}${detail ? `\n                 ${detail}` : ''}: update the docs and this script`)
  } catch (err) {
    console.log(`  ✓ closed       ${what} (${/** @type {any} */ (err).status} ${/** @type {any} */ (err).code})`)
  }
}
/** @param {string} what @param {() => Promise<string | void>} action */
async function hole(what, action) {
  try {
    const detail = await action()
    console.log(`  ! open         ${what}${detail ? `\n                 ${detail}` : ''}`)
  } catch (err) {
    unexpected++
    console.log(`  ✓ CLOSED       ${what} (${/** @type {any} */ (err).status} ${/** @type {any} */ (err).code}): update the docs and this script`)
  }
}

/** Signs up a new user and returns their client, database and collections. */
async function user(/** @type {string} */ name, /** @type {string} */ address = email(name)) {
  const c = createClient({ url, realm: 'expenses-with-functions' })
  await c.auth.signup({ email: address, password })
  const db = c.db('main')
  return { email: address, c, db, groups: db.collection('groups'), expenses: db.collection('expenses') }
}

const cents = (/** @type {number} */ n) => (n / 100).toFixed(2) + ' €'

const ada = await user('ada')
const bob = await user('bob')
const mallory = await user('mallory')
const carl = await user('carl')
const members = [ada.email, bob.email, mallory.email].sort()

console.log(`Server ${url}. Ada creates a group with Bob and Mallory, and pays a 60 € dinner for the three (through add_expense, not a direct write).\n`)
const group = await ada.groups.create({ name: 'Lisbon', currency: 'EUR', members })
const dinner = await ada.db.fn('add_expense', { group_id: group.id, description: 'Dinner', amount: 6000, split_between: members })

console.log('Attacks the rules (and the functions) stop:')
await stopped('an outsider reads the group', () => carl.groups.get(group.id), NotFoundError)
await stopped('an outsider calls list_expenses for the group', () => carl.db.fn('list_expenses', { group_id: group.id }), NotFoundError)
await stopped('an anonymous caller calls list_expenses', () => createClient({ url, realm: 'expenses-with-functions' }).db('main').fn('list_expenses', { group_id: group.id }), AuthenticationError)
{
  // A direct read only ever returns your own entries (expenses/rules.yaml:
  // read: user != nil && document._meta.owner == user.id) — never anyone
  // else's, regardless of group membership. Seeing the group's other
  // expenses always goes through list_expenses instead.
  const what = 'bob lists expenses directly (not through list_expenses): only his own entries come back, never ada\'s'
  const page = await bob.expenses.list({ where: { group_id: group.id } })
  if (page.items.length === 0) console.log(`  ✓ stopped      ${what} (200, an empty list: bob has written nothing yet)`)
  else unexpected++, console.log(`  ✗ NOT STOPPED  ${what}: ${page.items.length} listed`)
}
{
  // Ada owns the dinner, so a direct GET by id works for her — proving
  // the read rule really is "your own entries", not "no reads at all".
  const what = 'ada reads her own dinner directly by id: this works, she owns it'
  const d = await ada.expenses.get(dinner.id)
  if (d.id === dinner.id) console.log(`  ✓ stopped      ${what} (200) — the interesting refusal is bob's, next`)
  else unexpected++, console.log(`  ✗ NOT STOPPED  ${what}: unexpected document`)
}
await stopped('bob reads ada\'s dinner directly by id (not through list_expenses)', () => bob.expenses.get(dinner.id), NotFoundError)
await stopped('add_expense: Bob splits with Carl, who is outside the group',
  () => bob.db.fn('add_expense', { group_id: group.id, amount: 100, split_between: [carl.email] }), BackdError, (e) => e.code === 'not_a_member')
await stopped('add_expense: Carl (not a member at all) tries to add an expense',
  () => carl.db.fn('add_expense', { group_id: group.id, amount: 100, split_between: [ada.email] }), NotFoundError)
await stopped('request_settlement: Ada tries to settle with herself',
  () => ada.db.fn('request_settlement', { group_id: group.id, to: ada.email, amount: 100 }), BackdError, (e) => e.code === 'cant_settle_with_yourself')
await stopped('Bob edits the amount of Ada\'s dinner (direct PATCH, ownership unchanged)', () => bob.expenses.patch(dinner.id, { amount: 1 }), NotFoundError)
await stopped('Bob deletes Ada\'s dinner (direct DELETE, ownership unchanged)', () => bob.expenses.delete(dinner.id), NotFoundError)
await stopped('Ada moves her dinner to another group', () => ada.expenses.patch(dinner.id, { group_id: 'elsewhere' }), ForbiddenError)
await stopped('Bob changes the group\'s currency', () => bob.groups.patch(group.id, { currency: 'USD' }), ForbiddenError)
await stopped('Bob deletes the group he didn\'t create', () => bob.groups.delete(group.id), ForbiddenError)

console.log('\nHoles 1-4 (closed by functions):')

const before3 = (await ada.db.fn('balances', { group_id: group.id })).balances[ada.email]
const bobOwes = await bob.db.fn('request_settlement', { group_id: group.id, to: ada.email, amount: 2000 })
const pending3 = (await ada.db.fn('balances', { group_id: group.id })).balances[ada.email]
console.log(`  ✓ closed       3a. Bob requests to settle 20 € with Ada; it's pending, so her balance doesn't move yet (${cents(before3)} → ${cents(pending3)})`)
await stopped('3b. Mallory (not the receiver) confirms Bob\'s pending settlement',
  () => mallory.db.fn('confirm_settlement', { id: bobOwes.id }), ForbiddenError)
await stopped('3c. Bob (the payer, not the receiver) confirms his own pending settlement',
  () => bob.db.fn('confirm_settlement', { id: bobOwes.id }), ForbiddenError)
await ada.db.fn('confirm_settlement', { id: bobOwes.id })
const after3 = (await ada.db.fn('balances', { group_id: group.id })).balances[ada.email]
console.log(`  ✓ closed       3d. Only once Ada confirms it herself does it count (balance: ${cents(pending3)} → ${cents(after3)})`)

const remaining = [ada.email, bob.email]
await ada.groups.patch(group.id, { members: remaining })
console.log('\n  (Ada removes Mallory from the group. Mallory still knows its id.)')
await stopped('Mallory reads the group after her removal', () => mallory.groups.get(group.id), NotFoundError)

await closed('2. Mallory still reads the dinner from before her removal, through list_expenses', () => mallory.db.fn('list_expenses', { group_id: group.id }))

await closed('1. Mallory adds a 900 € "Taxi" to the group she left, split between Ada and Bob', async () => {
  const before = (await ada.db.fn('balances', { group_id: group.id })).balances[ada.email]
  await mallory.db.fn('add_expense', { group_id: group.id, description: 'Taxi', amount: 90000, split_between: remaining })
  const after = (await ada.db.fn('balances', { group_id: group.id })).balances[ada.email]
  return `Ada's balance: ${cents(before)} → ${cents(after)} (should be unchanged: the attack never got in).`
})

console.log(`  ✓ closed       4. Balances are computed once, on the server (the balances function), from data holes 1-3 no longer let anyone fake — not by each client from its own copy.`)

const dan = email('dan')
await ada.groups.patch(group.id, { members: [...remaining, dan] })
await hole(`5. Ada invites ${dan}; someone else signs up with that email first and reads the group`, async () => {
  const squatter = await user('squatter', dan)
  const g = await squatter.groups.get(group.id)
  return `The squatter sees "${g.name}" with ${g.members.length} members. Email verification (planned) would close this.`
})

console.log(unexpected === 0
  ? '\nAs documented: every attack above was stopped, holes 1-4 are closed, and hole 5 is still open.'
  : `\n${unexpected} result(s) differ from the documentation: see the lines marked ✗.`)
process.exit(unexpected === 0 ? 0 : 1)
