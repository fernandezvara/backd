import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, expect, test, vi } from 'vitest'
import { i18n } from '@/i18n'
import { ACCENTS, usePreferences } from '@/stores/preferences'
import AccentSwitch from './AccentSwitch.vue'

beforeEach(() => {
  // The page reads the system theme; jsdom has no media queries.
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }))
  localStorage.clear()
  setActivePinia(createPinia())
})

for (const compact of [false, true]) {
  test(`${compact ? 'the swatch row' : 'the named chips'} offer every accent and choosing one sets the preference`, async () => {
    const prefs = usePreferences()
    const w = mount(AccentSwitch, { props: { compact }, global: { plugins: [i18n] } })
    for (const a of ACCENTS) expect(w.find(`[data-testid="accent-${a}"]`).exists(), a).toBe(true)
    await w.get('[data-testid="accent-rose"]').trigger('click')
    expect(prefs.accent).toBe('rose')
    expect(w.get('[data-testid="accent-rose"]').attributes('aria-pressed')).toBe('true')
    expect(w.get('[data-testid="accent-teal"]').attributes('aria-pressed')).toBe('false')
  })
}
