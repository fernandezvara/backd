import { expect, test } from 'vitest'
import { errorText, formatDate, parseList, prettyDuration } from './format'

test('dates', () => {
  expect(formatDate(null)).toBe('—')
  expect(formatDate('not a date')).toBe('not a date')
  expect(formatDate('2026-10-01T10:00:00.000Z')).toMatch(/2026/)
  // The interface's language decides the month's name.
  expect(formatDate('2026-10-01T10:00:00.000Z', 'en')).toMatch(/Oct/)
  expect(formatDate('2026-10-01T10:00:00.000Z', 'es')).toMatch(/oct/)
})

test('error text prefers the server message', () => {
  expect(errorText(new Error('email already used'), 'x')).toBe('email already used')
  expect(errorText('boom', 'fallback')).toBe('fallback')
})

test('comma lists', () => {
  expect(parseList('a, b ,,c')).toEqual(['a', 'b', 'c'])
  expect(parseList('')).toEqual([])
})

test('durations', () => {
  expect(prettyDuration('720h0m0s')).toBe('30d')
  expect(prettyDuration('10m0s')).toBe('10m')
  expect(prettyDuration('1h30m0s')).toBe('1h30m')
  expect(prettyDuration('36h0m0s')).toBe('1d12h')
  expect(prettyDuration('10s')).toBe('10s')
  expect(prettyDuration('0s')).toBe('0s')
  expect(prettyDuration('soon')).toBe('soon')
})
