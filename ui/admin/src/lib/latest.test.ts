import { expect, test } from 'vitest'
import { latestOnly } from './latest'

test('only the newest request is current', () => {
  const latest = latestOnly()
  const first = latest.begin()
  expect(first()).toBe(true)
  const second = latest.begin()
  expect(first()).toBe(false)
  expect(second()).toBe(true)
})

test('an answer that arrives after a newer request started is ignored', async () => {
  const latest = latestOnly()
  const shown: string[] = []
  const load = async (name: string, delayMs: number) => {
    const current = latest.begin()
    await new Promise((r) => setTimeout(r, delayMs))
    if (!current()) return
    shown.push(name)
  }
  // The live list is slow and the trash list (asked later) is fast: the live answer arrives last.
  await Promise.all([load('live', 30), load('trash', 5)])
  expect(shown).toEqual(['trash'])
})
