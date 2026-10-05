import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { expect, test } from 'vitest'
import { nextTick } from 'vue'
import { i18n } from '@/i18n'
import type { Schema } from '@/lib/schema'
import SchemaForm from './SchemaForm.vue'

const schema: Schema = {
  type: 'object',
  properties: {
    title: { type: 'string', minLength: 1, description: 'The title.' },
    qty: { type: 'integer', minimum: 0 },
    live: { type: 'boolean' },
    kind: { enum: ['a', 'b'] },
    when: { type: 'string', format: 'date' },
    tags: { type: 'array', items: { type: 'string' } },
    author: { type: 'object', properties: { name: { type: 'string' } }, required: ['name'] },
    extra: { oneOf: [{ type: 'string' }, { type: 'number' }] },
  },
  required: ['title'],
}

function setup(value: Record<string, unknown>, props: Record<string, unknown> = {}) {
  const updates: Record<string, unknown>[] = []
  const wrapper = mount(SchemaForm, {
    props: { schema, modelValue: value, 'onUpdate:modelValue': (v: Record<string, unknown>) => { updates.push(v); void wrapper.setProps({ modelValue: v }) }, ...props },
    global: { plugins: [createPinia(), i18n] },
  })
  return { wrapper, updates }
}

test('draws the declared fields, marks the required ones and shows limits and help', () => {
  const { wrapper } = setup({ title: '' })
  const text = wrapper.text()
  for (const label of ['title', 'qty', 'live', 'kind', 'when', 'tags', 'author', 'extra']) expect(text).toContain(label)
  expect(wrapper.find('[data-path="title"] label').text()).toContain('*')
  expect(wrapper.find('[data-path="qty"] label').text()).not.toContain('*')
  expect(text).toContain('The title.')
  expect(text).toContain('at least 1 characters')
  expect(text).toContain('minimum 0')
  expect(wrapper.find('[data-path="when"] input').attributes('type')).toBe('date')
})

test('typing changes the model; an emptied optional field leaves the document', async () => {
  const { wrapper, updates } = setup({ title: 'Hi', note: 'kept' })
  await wrapper.find('[data-path="title"] textarea, [data-path="title"] input').setValue('Hello')
  expect(updates.at(-1)).toEqual({ title: 'Hello', note: 'kept' }) // fields the schema doesn't declare stay
  await wrapper.find('[data-path="qty"] input').setValue('5')
  expect(updates.at(-1)).toMatchObject({ qty: 5 })
  await wrapper.find('[data-path="qty"] input').setValue('')
  expect(updates.at(-1)).not.toHaveProperty('qty')
  await wrapper.find('[data-path="live"] select').setValue('true')
  expect(updates.at(-1)).toMatchObject({ live: true })
  await wrapper.find('[data-path="kind"] select').setValue('1')
  expect(updates.at(-1)).toMatchObject({ kind: 'b' })
})

test('arrays add, reorder and remove items', async () => {
  const { wrapper, updates } = setup({ title: 'x', tags: ['a', 'b'] })
  await wrapper.findAll('button').find((b) => b.text().startsWith('Add to tags'))!.trigger('click')
  expect(updates.at(-1)!.tags).toEqual(['a', 'b', ''])
  await wrapper.find('button[aria-label="Move tags 2 up"]').trigger('click')
  expect(updates.at(-1)!.tags).toEqual(['b', 'a', ''])
  await wrapper.find('button[aria-label="Remove tags 1"]').trigger('click')
  expect(updates.at(-1)!.tags).toEqual(['a', ''])
})

test('an optional object is added on demand, and its required fields start empty', async () => {
  const { wrapper, updates } = setup({ title: 'x' })
  await wrapper.findAll('button').find((b) => b.text() === 'Add author')!.trigger('click')
  expect(updates.at(-1)!.author).toEqual({ name: '' })
  await nextTick()
  expect(wrapper.find('[data-path="author.name"]').exists()).toBe(true)
})

test('what a form cannot draw is a JSON editor, and only valid JSON reaches the model', async () => {
  const { wrapper, updates } = setup({ title: 'x' })
  const ta = wrapper.find('[data-path="extra"] textarea')
  expect(wrapper.find('[data-path="extra"]').text()).toContain('oneOf')
  await ta.setValue('{nope')
  expect(wrapper.find('[data-path="extra"]').text()).toContain("isn't valid JSON")
  expect(updates.length).toBe(0)
  await ta.setValue('42')
  expect(updates.at(-1)).toMatchObject({ extra: 42 })
})

test('server errors appear next to their field, read-only disables editing', async () => {
  const { wrapper } = setup({ title: '', author: { name: '' } }, { errors: { title: 'is required', 'author.name': 'too short' } })
  expect(wrapper.find('[data-path="title"]').text()).toContain('is required')
  expect(wrapper.find('[data-path="author.name"]').text()).toContain('too short')
  expect(wrapper.find('[data-path="title"] [aria-invalid="true"]').exists()).toBe(true)
  await wrapper.setProps({ readonly: true })
  expect(wrapper.find('[data-path="kind"] select').attributes('disabled')).toBeDefined()
  expect(wrapper.find('[data-path="qty"] input').attributes('readonly')).toBeDefined()
  expect(wrapper.findAll('button').filter((b) => b.text().startsWith('Add')).length).toBe(0)
})
