import { expect, test } from 'vitest'
import { applyTheme } from './preferences'

test('system follows the OS, explicit choices win', () => {
  const root = document.createElement('html')
  applyTheme('system', root, true)
  expect(root.classList.contains('dark')).toBe(true)
  applyTheme('system', root, false)
  expect(root.classList.contains('dark')).toBe(false)
  applyTheme('dark', root, false)
  expect(root.classList.contains('dark')).toBe(true)
  applyTheme('light', root, true)
  expect(root.classList.contains('dark')).toBe(false)
})
