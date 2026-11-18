import { mount } from '@vue/test-utils'
import { expect, test } from 'vitest'
import SegmentedControl from './SegmentedControl.vue'

const options = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta' },
  { value: 'c', label: 'Gamma', testid: 'gamma' },
]
const setup = (value = 'a') => mount(SegmentedControl, { props: { label: 'Letters', options, modelValue: value, 'onUpdate:modelValue': (v: unknown) => w.setProps({ modelValue: v as string }) }, attachTo: document.body })
let w: ReturnType<typeof setup>

test('it is a radiogroup with one checked, focusable choice', () => {
  w = setup('b')
  expect(w.get('[role="radiogroup"]').attributes('aria-label')).toBe('Letters')
  const radios = w.findAll('[role="radio"]')
  expect(radios.map((r) => r.attributes('aria-checked'))).toEqual(['false', 'true', 'false'])
  expect(radios.map((r) => r.attributes('tabindex'))).toEqual(['-1', '0', '-1'])
  expect(w.find('[data-testid="gamma"]').exists()).toBe(true)
})

test('clicking a choice selects it', async () => {
  w = setup()
  await w.findAll('[role="radio"]')[2].trigger('click')
  expect(w.props('modelValue')).toBe('c')
})

test('the arrow keys move to the next and the previous choice, and stop at the ends', async () => {
  w = setup()
  const radios = () => w.findAll('[role="radio"]')
  await radios()[0].trigger('keydown', { key: 'ArrowRight' })
  expect(w.props('modelValue')).toBe('b')
  await radios()[1].trigger('keydown', { key: 'ArrowRight' })
  expect(w.props('modelValue')).toBe('c')
  await radios()[2].trigger('keydown', { key: 'ArrowRight' })
  expect(w.props('modelValue')).toBe('c')
  await radios()[2].trigger('keydown', { key: 'ArrowLeft' })
  expect(w.props('modelValue')).toBe('b')
})

test('icon-only choices keep their name for screen readers', () => {
  const icon = { render: () => null }
  const m = mount(SegmentedControl, { props: { label: 'Theme', iconOnly: true, options: [{ value: 'x', label: 'Light', icon }], modelValue: 'x' } })
  const r = m.get('[role="radio"]')
  expect(r.attributes('aria-label')).toBe('Light')
  expect(r.get('span').classes()).toContain('sr-only')
})
