import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, expect, test, vi } from 'vitest'
import { i18n } from '@/i18n'
import { useSession } from '@/stores/session'
import CollectionTriggers from './CollectionTriggers.vue'

const calls: string[][] = []
const status = {
  triggers: [
    { database: 'main', collection: 'orders', name: 'receipt', events: ['create', 'update'], when: "doc.total > 100", function: 'main/send_receipt', fields: null, session: false, ordered: true },
    { database: 'main', collection: 'orders', name: 'audit', events: ['delete'], when: null, function: 'ops/audit_log', fields: null, session: false, ordered: false },
    { database: 'main', collection: 'notes', name: 'other', events: ['create'], when: null, function: 'main/x', fields: null, session: false, ordered: false },
  ],
}
vi.mock('@/lib/adminRaw', () => ({ useRawAdmin: () => async (_m: string, path: string[]) => (calls.push(path), status) }))
vi.mock('@/lib/admin', () => ({ useAdmin: () => ({}) }))

const stub = { RouterLink: { props: ['to'], template: '<a :data-to="JSON.stringify(to)"><slot /></a>' } }
const mountIt = () => mount(CollectionTriggers, { props: { realm: 'acme', database: 'main', collection: 'orders' }, global: { plugins: [i18n], stubs: stub } })

beforeEach(() => {
  calls.length = 0
  setActivePinia(createPinia())
})

test('it lists the triggers of this collection only, each linked to its function', async () => {
  const s = useSession()
  vi.spyOn(s, 'canRead').mockReturnValue(true)
  const w = mountIt()
  await flushPromises()
  expect(calls).toEqual([['triggers', 'status']])
  const rows = w.findAll('tbody tr')
  expect(rows).toHaveLength(2)
  expect(rows[0].text()).toContain('receipt')
  expect(rows[0].text()).toContain('doc.total > 100')
  expect(rows[1].text()).toContain('audit')
  expect(w.text()).not.toContain('other')
  const link = rows[0].get('a[data-to]')
  expect(JSON.parse(link.attributes('data-to')!)).toEqual({ name: 'function', params: { realm: 'acme', database: 'main', name: 'send_receipt' } })
})

test('without the right to read functions it asks nothing and says why', async () => {
  const s = useSession()
  vi.spyOn(s, 'canRead').mockReturnValue(false)
  const w = mountIt()
  await flushPromises()
  expect(calls).toEqual([])
  expect(w.find('[data-testid="collection-triggers"]').exists()).toBe(false)
  expect(w.text()).toContain(String(i18n.global.t('data.triggers.needsFunctions')))
})
