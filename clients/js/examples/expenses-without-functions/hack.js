// Attacks the expenses example the way a malicious user could, with the
// same client library the app uses, and reports what happened:
//
//   - attacks the access rules stop (examples/config/expenses/*/rules.yaml);
//   - holes that stay open because this example doesn't use server-side
//     functions (the "with functions" version closes them), and one that
//     email verification closes. See the docs page "Expenses without
//     functions".
//
// It exits with 0 when every attack is stopped and every known hole is
// open, which is the expected state today. When a future version closes a
// hole, this script says so and exits with 1: update the docs and the
// expectations below.
//
//   make example                   # in another terminal
//   make hack-expenses             # or:
//   NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/expenses-without-functions/hack.js
//
// BACKD_URL overrides the server (default https://localhost:8443). Every
// run signs up new users with unique emails and verifies them through the
// realm's development outbox, like a person following the link in the email
// (a worker must be running, as in the local stack).
import { createClient, AuthenticationError, ForbiddenError, NotFoundError, VerificationRequiredError } from '../../src/index.js'
import { balances } from './ledger.js'
import { signupVerified } from '../verify-email.js'

const url = process.env.BACKD_URL ?? 'https://localhost:8443'
const run = Date.now().toString(36)
const email = (/** @type {string} */ name) => `${name}.${run}@example.com`
const password = 'dev-p4ssw0rd!'

let unexpected = 0
/** @param {string} what @param {() => Promise<unknown>} attack @param {Function} expected */
async function stopped(what, attack, expected) {
  try {
    await attack()
    unexpected++
    console.log(`  ✗ NOT STOPPED  ${what}`)
  } catch (err) {
    if (!(err instanceof expected)) throw err
    console.log(`  ✓ stopped      ${what} (${/** @type {any} */ (err).status} ${/** @type {any} */ (err).code})`)
  }
}
/** @param {string} what @param {() => Promise<string | void>} attack */
async function hole(what, attack) {
  try {
    const detail = await attack()
    console.log(`  ! open         ${what}${detail ? `\n                 ${detail}` : ''}`)
  } catch (err) {
    unexpected++
    console.log(`  ✓ CLOSED       ${what} (${/** @type {any} */ (err).status} ${/** @type {any} */ (err).code}): update the docs and this script`)
  }
}

/** Signs up a new user, verifies their address and returns their client and collections. */
async function user(/** @type {string} */ name, /** @type {string} */ address = email(name)) {
  const c = await signupVerified({ url, realm: 'expenses', email: address, password })
  const db = c.db('main')
  return { email: address, c, groups: db.collection('groups'), expenses: db.collection('expenses') }
}

/** A sign-up in this realm gives no session. */
async function assertPending(/** @type {Promise<unknown>} */ signup) {
  try {
    await signup
  } catch (err) {
    if (err instanceof VerificationRequiredError) return
    throw err
  }
  throw new Error('the sign-up started a session')
}

const cents = (/** @type {number} */ n) => (n / 100).toFixed(2) + ' €'

const ada = await user('ada')
const bob = await user('bob')
const mallory = await user('mallory')
const carl = await user('carl')
const members = [ada.email, bob.email, mallory.email].sort()

console.log(`Server ${url}. Ada creates a group with Bob and Mallory, and pays a 60 € dinner for the three.\n`)
const group = await ada.groups.create({ name: 'Lisbon', currency: 'EUR', members })
const expense = (/** @type {Record<string, unknown>} */ fields) => ({ kind: 'expense', group_id: group.id, members, ...fields })
const dinner = await ada.expenses.create(expense({ description: 'Dinner', amount: 6000, paid_by: ada.email, split_between: members }))

console.log('Attacks the rules stop:')
await stopped('an outsider reads the group', () => carl.groups.get(group.id), NotFoundError)
{
  // Read rules apply on top of any where: an $or can only narrow.
  const what = 'an outsider lists the group\'s expenses, even with an $or that matches everything'
  const page = await carl.expenses.list({ where: { $or: [{ group_id: group.id }, { group_id: { $ne: group.id } }] } })
  if (page.items.length === 0) console.log(`  ✓ stopped      ${what} (200, an empty list)`)
  else unexpected++, console.log(`  ✗ NOT STOPPED  ${what}: ${page.items.length} listed`)
}
await stopped('an anonymous caller lists expenses', () => createClient({ url, realm: 'expenses' }).db('main').collection('expenses').list(), AuthenticationError)
await stopped('Bob records an expense that Ada "paid"',
  () => bob.expenses.create(expense({ description: 'Hotel', amount: 50000, paid_by: ada.email, split_between: [bob.email] })), ForbiddenError)
await stopped('Ada splits an expense with someone outside the group',
  () => ada.expenses.create(expense({ amount: 100, paid_by: ada.email, split_between: [carl.email] })), ForbiddenError)
await stopped('Ada records a settlement to herself',
  () => ada.expenses.create(expense({ kind: 'settlement', amount: 100, paid_by: ada.email, split_between: [ada.email] })), ForbiddenError)
await stopped('Bob edits the amount of Ada\'s dinner', () => bob.expenses.patch(dinner.id, { amount: 1 }), ForbiddenError)
await stopped('Bob deletes Ada\'s dinner', () => bob.expenses.delete(dinner.id), ForbiddenError)
await stopped('Ada moves her dinner to another group', () => ada.expenses.patch(dinner.id, { group_id: 'elsewhere' }), ForbiddenError)
await stopped('Bob changes the group\'s currency', () => bob.groups.patch(group.id, { currency: 'USD' }), ForbiddenError)
await stopped('Bob deletes the group he didn\'t create', () => bob.groups.delete(group.id), ForbiddenError)

console.log('\nHoles this example leaves open (server-side functions would close them):')
await hole('3. Mallory records "Mallory paid Ada 20 €", which Ada never received or confirmed', async () => {
  await mallory.expenses.create(expense({ kind: 'settlement', description: 'Settlement', amount: 2000, paid_by: mallory.email, split_between: [ada.email] }))
  return 'Mallory now owes nothing; Ada is out 20 €.'
})

const remaining = [ada.email, bob.email]
await ada.groups.patch(group.id, { members: remaining })
console.log('\n  (Ada removes Mallory from the group. Mallory still knows its id.)')
await stopped('Mallory reads the group after her removal', () => mallory.groups.get(group.id), NotFoundError)

await hole('2. Mallory still reads the dinner from before her removal (its members copy is stale)', async () => {
  const d = await mallory.expenses.get(dinner.id)
  return `"${d.description}", ${cents(d.amount)}`
})

await hole('1. Mallory adds a 900 € "Taxi" to the group she left, split between Ada and Bob', async () => {
  const before = balances((await ada.expenses.list({ where: { group_id: group.id }, limit: 100 })).items)[ada.email]
  await mallory.expenses.create(expense({ description: 'Taxi', amount: 90000, paid_by: mallory.email, split_between: remaining }))
  const after = balances((await ada.expenses.list({ where: { group_id: group.id }, limit: 100 })).items)[ada.email]
  return `Ada's balance, as her app computes it: ${cents(before)} → ${cents(after)}.`
})

const dan = email('dan')
await ada.groups.patch(group.id, { members: [...remaining, dan] })
console.log('\nAnd the one email verification closes:')
await stopped(`5. Ada invites ${dan}; someone else signs up with that email first, without access to its mailbox`, async () => {
  const squatter = createClient({ url, realm: 'expenses' })
  await assertPending(squatter.auth.signup({ email: dan, password }))
  await squatter.auth.login({ email: dan, password }) // no session until the address is verified
}, ForbiddenError)

console.log(`  ! open         4. Balances are computed by each client: the fake entries above change everyone's numbers,
                 and there is no authoritative balance on the server.`)

console.log(unexpected === 0
  ? '\nAs documented: every attack above was stopped, and the known holes are open.'
  : `\n${unexpected} result(s) differ from the documentation: see the lines marked ✗ or CLOSED.`)
process.exit(unexpected === 0 ? 0 : 1)
