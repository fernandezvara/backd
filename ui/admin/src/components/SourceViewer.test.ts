import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { expect, test } from 'vitest'
import { i18n } from '@/i18n'
import SourceViewer from './SourceViewer.vue'

const file = (content: string | null, extra: Record<string, unknown> = {}) => ({ language: 'js', content, truncated: false, binary: false, ...extra })
const setup = (f: ReturnType<typeof file> | undefined, fit = false) => mount(SourceViewer, { props: { name: 'index.ts', file: f, fit }, global: { plugins: [createPinia(), i18n] } })

test('line numbers sit in their own grey gutter, one per line', () => {
  const w = setup(file('one\ntwo\nthree\n'))
  const [gutter] = w.findAll('pre')
  expect(gutter.text().split('\n')).toEqual(['1', '2', '3'])
  expect(gutter.classes()).toContain('bg-slate-100')
  expect(gutter.attributes('aria-hidden')).toBe('true')
})

test('by default the code scrolls inside its box; with fit the box grows to the code', () => {
  expect(setup(file('x')).get('[data-testid="fn-source"]').classes()).toContain('overflow-auto')
  const fit = setup(file('x'), true).get('[data-testid="fn-source"]')
  expect(fit.classes()).not.toContain('overflow-auto')
  expect(fit.attributes('tabindex')).toBeUndefined()
})

test('the notices say what is left out', () => {
  expect(setup(file(null, { binary: true })).text()).toContain(String(i18n.global.t('fn.code.binary')))
  expect(setup(file('x', { truncated: true })).text()).toContain(String(i18n.global.t('fn.code.truncated')))
  expect(setup(file('', {})).text()).toContain(String(i18n.global.t('fn.code.empty')))
})
