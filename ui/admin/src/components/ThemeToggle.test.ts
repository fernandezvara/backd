import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, expect, test, vi } from 'vitest'
import { i18n } from '@/i18n'
import { usePreferences } from '@/stores/preferences'
import ThemeToggle from './ThemeToggle.vue'

beforeEach(() => {
  // The page reads the system theme; jsdom has no media queries.
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }))
  localStorage.clear()
  setActivePinia(createPinia())
})

test('each click moves to the next theme: system, light, dark, and round again', async () => {
  const prefs = usePreferences()
  prefs.theme = 'system'
  const w = mount(ThemeToggle, { global: { plugins: [i18n] } })
  const seen: string[] = []
  for (let i = 0; i < 4; i++) {
    await w.get('[data-testid="theme-toggle"]').trigger('click')
    seen.push(prefs.theme)
  }
  expect(seen).toEqual(['light', 'dark', 'system', 'light'])
})

test('the button names the current theme', async () => {
  const prefs = usePreferences()
  prefs.theme = 'dark'
  const w = mount(ThemeToggle, { global: { plugins: [i18n] } })
  expect(w.get('[data-testid="theme-toggle"]').attributes('aria-label')).toMatch(/dark|oscuro/i)
})
