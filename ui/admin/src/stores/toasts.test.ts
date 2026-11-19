import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { useToasts } from './toasts'

beforeEach(() => {
  vi.useFakeTimers()
  setActivePinia(createPinia())
})
afterEach(() => vi.useRealTimers())

test('a confirmation goes away by itself', () => {
  const t = useToasts()
  t.push('Saved')
  expect(t.items).toHaveLength(1)
  vi.advanceTimersByTime(6000)
  expect(t.items).toHaveLength(0)
})

test('an error stays until it is dismissed', () => {
  const t = useToasts()
  t.push('Nope', 'error')
  vi.advanceTimersByTime(60_000)
  expect(t.items.map((x) => x.text)).toEqual(['Nope'])
  t.dismiss(t.items[0].id)
  expect(t.items).toEqual([])
})

test('toasts have their own ids and dismissing one keeps the others', () => {
  const t = useToasts()
  t.push('a', 'info', 0)
  t.push('b', 'info', 0)
  t.dismiss(t.items[0].id)
  expect(t.items.map((x) => x.text)).toEqual(['b'])
})
