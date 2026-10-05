import { expect, test } from 'vitest'
import { errorText, formatDate } from './format'

test('dates', () => {
  expect(formatDate(null)).toBe('—')
  expect(formatDate('not a date')).toBe('not a date')
  expect(formatDate('2026-10-01T10:00:00.000Z')).toMatch(/2026/)
})

test('error text prefers the server message', () => {
  expect(errorText(new Error('email already used'), 'x')).toBe('email already used')
  expect(errorText('boom', 'fallback')).toBe('fallback')
})
