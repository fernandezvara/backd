import { test } from 'node:test'
import assert from 'node:assert/strict'

import { balances, settlementPlan, toCents } from '../examples/expenses-without-functions/ledger.js'
import { toCents as toCentsWithFunctions } from '../examples/expenses-with-functions/ledger.js'

/** @param {string} paid_by @param {number} amount @param {string[]} split @returns {import('../examples/expenses-without-functions/ledger.js').Entry} */
const expense = (paid_by, amount, split) => ({ kind: 'expense', paid_by, amount, split_between: split })

test('balances split exactly, remainders included', () => {
  const net = balances([expense('ada', 1000, ['ada', 'bob', 'cy'])])
  assert.deepEqual(net, { ada: 1000 - 334, bob: -333, cy: -333 })
  assert.equal(Object.values(net).reduce((a, b) => a + b, 0), 0, 'balances always sum to zero')
})

test('a settlement pays a debt back', () => {
  const net = balances([
    expense('ada', 6000, ['ada', 'bob', 'cy']),
    { kind: 'settlement', paid_by: 'bob', amount: 2000, split_between: ['ada'] },
  ])
  assert.deepEqual(net, { ada: 2000, bob: 0, cy: -2000 })
})

test('the settlement plan settles everyone in few payments', () => {
  const net = balances([
    expense('ada', 9000, ['ada', 'bob', 'cy']),
    expense('bob', 3000, ['ada', 'bob', 'cy']),
    expense('cy', 600, ['cy', 'dan']),
  ])
  const plan = settlementPlan(net)
  const after = { ...net }
  for (const p of plan) {
    after[p.from] += p.amount
    after[p.to] -= p.amount
  }
  assert.ok(Object.values(after).every((v) => v === 0), 'everyone is settled')
  assert.ok(plan.length <= Object.keys(net).length - 1, 'at most one payment fewer than people')
  assert.ok(plan.every((p) => p.amount > 0 && p.from !== p.to))
})

test('amounts typed by people', () => {
  assert.equal(toCents('12.5'), 1250)
  assert.equal(toCents('12,50'), 1250)
  assert.equal(toCents(' 0.1 '), 10)
  assert.equal(toCents('19.99'), 1999)
  for (const bad of ['', '0', '-3', 'abc', 'Infinity']) assert.equal(toCents(bad), 0, bad)
})

// expenses-with-functions/ledger.js keeps only toCents (balances and the
// settlement plan moved server-side, to the balances function); same
// behavior, so the same cases apply.
test('amounts typed by people (expenses-with-functions)', () => {
  assert.equal(toCentsWithFunctions('12.5'), 1250)
  assert.equal(toCentsWithFunctions('12,50'), 1250)
  for (const bad of ['', '0', '-3', 'abc']) assert.equal(toCentsWithFunctions(bad), 0, bad)
})
