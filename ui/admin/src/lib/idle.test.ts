import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { watchIdle } from './idle'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

test('fires after the idle time', () => {
  const onIdle = vi.fn()
  watchIdle(60, onIdle)
  vi.advanceTimersByTime(59_000)
  expect(onIdle).not.toHaveBeenCalled()
  vi.advanceTimersByTime(2_000)
  expect(onIdle).toHaveBeenCalledTimes(1)
})

test('activity postpones it, and it fires once', () => {
  const onIdle = vi.fn()
  watchIdle(60, onIdle)
  vi.advanceTimersByTime(50_000)
  window.dispatchEvent(new Event('keydown'))
  vi.advanceTimersByTime(50_000)
  expect(onIdle).not.toHaveBeenCalled()
  vi.advanceTimersByTime(20_000)
  expect(onIdle).toHaveBeenCalledTimes(1)
  vi.advanceTimersByTime(600_000)
  expect(onIdle).toHaveBeenCalledTimes(1)
})

test('stop cancels it', () => {
  const onIdle = vi.fn()
  watchIdle(60, onIdle).stop()
  vi.advanceTimersByTime(3_600_000)
  expect(onIdle).not.toHaveBeenCalled()
})
