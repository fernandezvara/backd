import type { Doc } from 'backd-js'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { expect, test, vi } from 'vitest'
import { i18n } from '@/i18n'
import type { FileFieldConfig } from '@/stores/config'
import DocumentFiles from './DocumentFiles.vue'

// The component only reads and shows here: nothing is sent.
vi.mock('@/lib/admin', () => ({ useAdmin: () => ({ data: () => ({}) }) }))

const field = (extra: Partial<FileFieldConfig> = {}): FileFieldConfig => ({ multiple: false, max_files: 1, max_size: 65536, types: ['image/jpeg', 'image/png'], upload: 'proxy', download: null, ...extra })
const file = (extra: Record<string, unknown> = {}) => ({ id: 'fl_1', name: 'Cover.png', size: 120, type: 'image/png', ...extra })

function setup(fields: Record<string, FileFieldConfig>, doc: Record<string, unknown>) {
  return mount(DocumentFiles, {
    props: { database: 'main', collection: 'pics', doc: { id: 'd1', _meta: { version: 1 }, ...doc } as unknown as Doc, fields, writable: false },
    global: { plugins: [createPinia(), i18n] },
  })
}

test('a public file shows its permanent address, which opens in a new tab, and the field says it is public', () => {
  const url = 'https://cdn.example.com/prod/acme/main/pics/fl_1/Cover.png'
  const w = setup({ photo: field({ public: true }) }, { photo: file({ public: true, url }) })
  expect(w.find('[data-testid="field-public"]').exists()).toBe(true)
  const link = w.find('[data-testid="file-public-url"] a')
  expect(link.attributes('href')).toBe(url)
  expect(link.attributes('target')).toBe('_blank')
  expect(link.attributes('rel')).toContain('noopener')
  // The preview comes from the same address.
  expect(w.find('img').attributes('src')).toBe(url)
})

test('a private file shows no public address', () => {
  const w = setup({ scan: field() }, { scan: file({ url: 'https://files.example.com/signed?x=1' }) })
  expect(w.find('[data-testid="field-public"]').exists()).toBe(false)
  expect(w.find('[data-testid="file-public-url"]').exists()).toBe(false)
})
