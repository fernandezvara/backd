import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, expect, test, vi } from 'vitest'
import { i18n } from '@/i18n'
import { useSession } from '@/stores/session'
import SchemaVersionsPanel from './SchemaVersionsPanel.vue'

const report = { job_id: 'j1', database: 'main', collection: 'assets', target: 3, dry_run: false, status: 'done', ending: 'partial', created_at: '', finished_at: '', scanned: 10, migrated: 9, skipped: 0, failed: 1, failures: [{ id: 'a7', step: '2 → 3', kind: 'invalid', path: 'title', keyword: 'is required' }] }
const admin = {
  dataMigrations: {
    list: vi.fn(),
    get: vi.fn(),
    start: vi.fn(),
  },
  dataChecks: { get: vi.fn() },
}
vi.mock('@/lib/admin', () => ({ useAdmin: () => admin }))

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  admin.dataMigrations.list.mockResolvedValue({ running: [], items: [{ database: 'main', collection: 'assets', current_version: 3, documents_below_current: 12, report }] })
  admin.dataMigrations.get.mockResolvedValue(report)
  admin.dataChecks.get.mockResolvedValue({ by_version: { '1': 3, '3': 40 } })
  admin.dataMigrations.start.mockResolvedValue({ already_running: false })
})

function mountPanel(canWrite = true) {
  const session = useSession()
  vi.spyOn(session, 'canWrite').mockReturnValue(canWrite)
  return mount(SchemaVersionsPanel, { props: { realm: 'shop', database: 'main', collection: 'assets' }, global: { plugins: [i18n] } })
}

test('shows the counts per version, how many are below the current one and the latest migration with its failures', async () => {
  const w = mountPanel()
  await flushPromises()
  expect(w.find('[data-testid="below-current"]').text()).toContain('12')
  expect(w.find('[data-testid="by-version"]').text()).toContain('v1: 3')
  expect(w.find('[data-testid="by-version"]').text()).toContain('v3: 40')
  const latest = w.find('[data-testid="latest-migration"]').text()
  expect(latest).toContain('partial')
  expect(latest).toContain('migrated 9')
  expect(latest).toContain('a7: 2 → 3, invalid title is required')
})

test('"Migrate now" asks first and then starts the migration', async () => {
  const w = mountPanel()
  await flushPromises()
  await w.find('[data-testid="migrate-now"]').trigger('click')
  expect(w.find('[data-testid="migrate-confirm"]').exists()).toBe(true)
  expect(admin.dataMigrations.start).not.toHaveBeenCalled()
  await w.find('[data-testid="migrate-confirm-go"]').trigger('click')
  await flushPromises()
  expect(admin.dataMigrations.start).toHaveBeenCalledWith({ database: 'main', collection: 'assets', dryRun: false })
  expect(w.find('[data-testid="migrate-confirm"]').exists()).toBe(false)
})

test('a dry run is started with dryRun', async () => {
  const w = mountPanel()
  await flushPromises()
  await w.find('[data-testid="migrate-dry"]').trigger('click')
  await w.find('[data-testid="migrate-confirm-go"]').trigger('click')
  await flushPromises()
  expect(admin.dataMigrations.start).toHaveBeenCalledWith({ database: 'main', collection: 'assets', dryRun: true })
})

test('a reader without write access sees the state but no buttons', async () => {
  const w = mountPanel(false)
  await flushPromises()
  expect(w.find('[data-testid="schema-versions"]').exists()).toBe(true)
  expect(w.find('[data-testid="migrate-now"]').exists()).toBe(false)
})

test('a collection with no migrations has no panel', async () => {
  admin.dataMigrations.list.mockResolvedValue({ running: [], items: [] })
  const w = mountPanel()
  await flushPromises()
  expect(w.find('[data-testid="schema-versions"]').exists()).toBe(false)
})
