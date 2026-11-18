<script setup lang="ts">
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import { PhBuildings, PhCaretLeft, PhCaretRight, PhFunnel, PhPlus, PhSealCheck, PhTable } from '@phosphor-icons/vue'
import type { AdminOrg, Doc } from 'backd-js'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FormDialog from '@/components/FormDialog.vue'
import JsonView from '@/components/JsonView.vue'
import SchemaCheckPanel from '@/components/SchemaCheckPanel.vue'
import CollectionApi from '@/components/CollectionApi.vue'
import CollectionTriggers from '@/components/CollectionTriggers.vue'
import CollectionDefinition from '@/components/CollectionDefinition.vue'
import SchemaVersionsPanel from '@/components/SchemaVersionsPanel.vue'
import SubTabs from '@/components/SubTabs.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { buildWhere, operatorsFor, orderBy, queryFields, QueryError, type Condition } from '@/lib/query'
import { resolve, type Schema } from '@/lib/schema'
import { latestOnly } from '@/lib/latest'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string; database: string; collection: string }>()
const { t } = useI18n()
const admin = useAdmin()
const cfg = useRealmConfig()
const session = useSession()
const toasts = useToasts()

const canWrite = computed(() => session.canWrite('data'))
const info = computed(() => cfg.collectionInfo(props.database, props.collection))
const schema = computed(() => info.value?.schema as Schema | undefined)
const softDelete = computed(() => info.value?.softDelete ?? false)
const fields = computed(() => queryFields(schema.value))

// A collection that adopted organizations after it had documents: those have none, and members
// can't see them. An administrator gives them one (a job; nothing that has an organization moves).
const orgMode = computed(() => info.value?.organization ?? null)
const assigning = ref(false)
const assignOrgs = ref<AdminOrg[]>([])
const assignTo = ref('')
const assignMatched = ref<number | null>(null)
const assignBusy = ref(false)
const assignError = ref('')
async function openAssign() {
  assignTo.value = ''
  assignMatched.value = null
  assignError.value = ''
  assigning.value = true
  try {
    assignOrgs.value = await admin.orgs.list({ status: 'active' })
  } catch (e) {
    assignError.value = errorText(e, t('orgs.loadFailed'))
  }
}
watch(assignTo, async (org) => {
  assignMatched.value = null
  if (!org) return
  try {
    assignMatched.value = (await admin.orgs.assign(props.database, props.collection, org, { dryRun: true })).matched
  } catch (e) {
    assignError.value = errorText(e, t('common.unexpected'))
  }
})
async function assign() {
  assignBusy.value = true
  assignError.value = ''
  try {
    await admin.orgs.assign(props.database, props.collection, assignTo.value)
    assigning.value = false
    toasts.push(t('data.collection.assign.done'))
  } catch (e) {
    assignError.value = errorText(e, t('common.unexpected'))
  } finally {
    assignBusy.value = false
  }
}

// The columns: the first few scalar fields the schema declares.
const columns = computed<string[]>(() => {
  const s = schema.value
  if (!s?.properties) return []
  return Object.entries(s.properties as Record<string, Schema>)
    .filter(([, p]) => ['string', 'number', 'integer', 'boolean', 'enum'].includes(resolve(p, s).kind))
    .map(([k]) => k)
    .slice(0, 4)
})

// Query state
const conditions = reactive<Condition[]>([])
const rawOpen = ref(false)
const raw = ref('')
const sortField = ref('')
const sortDir = ref<'asc' | 'desc'>('asc')
const limit = ref(20)
const view = ref<'live' | 'trash'>('live')
const latest = latestOnly()
const asJson = ref(false)

const docs = ref<Doc[]>([])
const total = ref<number | undefined>()
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')
const queryProblem = ref('')

// Paging: a cursor when the order allows it, an offset when not.
const starts = ref<{ after?: string; skip: number }[]>([{ skip: 0 }])
const pageIndex = ref(0)
let nextStart: { after?: string; skip: number } | undefined

function where(): object | string | undefined {
  if (rawOpen.value) {
    if (!raw.value.trim()) return undefined
    JSON.parse(raw.value) // a syntax error is reported before anything is sent
    return raw.value
  }
  const w = buildWhere(conditions, fields.value)
  return Object.keys(w).length ? w : undefined
}

async function load() {
  queryProblem.value = ''
  let w: object | string | undefined
  try {
    w = where()
  } catch (e) {
    queryProblem.value = t('data.collection.queryProblem', { why: e instanceof QueryError ? e.message : t('data.form.invalidJson') })
    return
  }
  loading.value = true
  error.value = ''
  const current = latest.begin()
  const start = starts.value[pageIndex.value]
  try {
    const page = await admin.data(props.database, props.collection).list({
      where: w,
      orderBy: orderBy(sortField.value, sortDir.value),
      limit: limit.value,
      skip: start.after === undefined ? start.skip : undefined,
      after: start.after,
      count: true,
      deleted: view.value === 'trash' ? 'only' : undefined,
    })
    if (!current()) return // a newer load started (Trash, a page, a filter): this answer is stale
    docs.value = page.items
    hasMore.value = page.has_more
    total.value = page.total
    nextStart = page.has_more ? (page.next_cursor ? { after: page.next_cursor, skip: 0 } : { skip: start.skip + limit.value }) : undefined
  } catch (e) {
    if (current()) error.value = errorText(e, t('data.collection.loadFailed'))
  } finally {
    if (current()) loading.value = false
  }
}
function apply() {
  starts.value = [{ skip: 0 }]
  pageIndex.value = 0
  void load()
}
function clear() {
  conditions.splice(0)
  raw.value = ''
  sortField.value = ''
  sortDir.value = 'asc'
  apply()
}
function page(delta: -1 | 1) {
  if (delta === 1 && nextStart) {
    starts.value = [...starts.value.slice(0, pageIndex.value + 1), nextStart]
    pageIndex.value++
  } else if (delta === -1 && pageIndex.value > 0) {
    pageIndex.value--
  } else return
  void load()
}
function switchView(v: 'live' | 'trash') {
  view.value = v
  apply()
}
onMounted(async () => {
  await cfg.load()
  await load()
})

function addCondition() {
  const first = fields.value[0]
  conditions.push({ field: first?.path ?? 'id', op: '$eq', value: '' })
}
const fieldOf = (c: Condition) => fields.value.find((f) => f.path === c.field)
function onFieldChange(c: Condition) {
  const f = fieldOf(c)
  if (f && !operatorsFor(f.type).includes(c.op)) c.op = '$eq'
  c.value = ''
}
const valueKind = (c: Condition): 'bool' | 'enum' | 'date' | 'text' => {
  const f = fieldOf(c)
  if (c.op === '$isNull' || c.op === '$notNull') return 'bool'
  if (!f) return 'text'
  if (f.type === 'boolean' && c.op !== '$in') return 'bool'
  if (f.type === 'enum' && (c.op === '$eq' || c.op === '$ne')) return 'enum'
  if (f.type === 'date' && c.op !== '$between' && c.op !== '$in' && c.op !== '$nin') return 'date'
  return 'text'
}

function cell(doc: Doc, key: string): string {
  const v = (doc as Record<string, unknown>)[key]
  if (v === undefined || v === null) return '—'
  const text = typeof v === 'object' ? JSON.stringify(v) : String(v)
  return text.length > 80 ? `${text.slice(0, 80)}…` : text
}

async function restore(doc: Doc) {
  try {
    await admin.data(props.database, props.collection).restore(doc.id)
    toasts.push(t('data.collection.restored'))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
// The schema check stays folded until asked for.
const showCheck = ref(false)
const filterOpen = ref(false)
const activeFilters = computed(() => (rawOpen.value ? (raw.value.trim() ? 1 : 0) : conditions.length))
// "1–20 of 4,032": where this page sits in the result.
const range = computed(() => {
  if (!docs.value.length) return total.value === undefined ? '' : t('data.collection.total', { n: total.value })
  const from = starts.value[pageIndex.value].skip + (starts.value[pageIndex.value].after ? pageIndex.value * limit.value : 0) + 1
  const to = from + docs.value.length - 1
  return total.value === undefined ? `${from}–${to}` : t('data.collection.range', { from, to, n: total.value.toLocaleString() })
})
const tab = ref('records')
</script>

<template>
  <div class="mb-1 flex flex-wrap items-center gap-3">
    <PhTable :size="26" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />
    <div>
      <h1 class="font-mono text-lg font-semibold leading-tight" data-testid="collection-name">{{ collection }}</h1>
      <p class="text-xs text-slate-500">{{ database }}<template v-if="total !== undefined"> · {{ view === 'trash' ? t('data.collection.inTrash', { n: total.toLocaleString() }) : t('data.collection.documents', { n: total.toLocaleString() }) }}</template></p>
    </div>
    <div class="ml-auto flex flex-wrap items-center gap-2">
      <AppButton variant="secondary" :aria-expanded="showCheck" aria-controls="schema-check" data-testid="check-toggle" @click="showCheck = !showCheck"><PhSealCheck :size="15" aria-hidden="true" />{{ showCheck ? t('checks.hide') : t('checks.show') }}</AppButton>
      <AppButton v-if="canWrite && orgMode" variant="secondary" @click="openAssign"><PhBuildings :size="15" aria-hidden="true" />{{ t('data.collection.assign.open') }}</AppButton>
      <RouterLink v-if="canWrite" :to="{ name: 'data-new', params: { realm, database, collection } }" class="inline-flex h-9 items-center gap-1.5 rounded-md bg-brand-700 px-3.5 text-sm font-medium text-white hover:bg-brand-800"><PhPlus aria-hidden="true" /> {{ t('data.collection.new') }}</RouterLink>
    </div>
  </div>

  <SubTabs v-model="tab" class="mt-3" :label="t('data.tabs.label')" :tabs="[{ id: 'records', label: t('data.tabs.records') }, { id: 'schema', label: t('data.tabs.schema') }, { id: 'rules', label: t('data.tabs.rules') }, { id: 'triggers', label: t('data.tabs.triggers') }, { id: 'api', label: t('data.tabs.api') }]" />
  <SchemaCheckPanel v-if="showCheck" :realm="realm" :database="database" :collection="collection" :total="conditions.length || view === 'trash' ? undefined : total" class="mb-4" />

  <template v-if="tab === 'schema'">
    <div class="grid gap-5 xl:grid-cols-5">
      <AppCard flush class="xl:col-span-3">
        <h3 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('data.schema.fields') }}</h3>
        <table class="w-full text-left text-[13px]" data-testid="schema-fields">
          <thead>
            <tr><th scope="col" class="px-4 py-2">{{ t('data.schema.name') }}</th><th scope="col" class="px-2 py-2">{{ t('data.schema.type') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="f in fields" :key="f.path" class="border-t border-slate-100 dark:border-slate-800">
              <td class="px-4 py-1.5 font-mono text-xs">{{ f.path }}</td>
              <td class="px-2 py-1.5"><AppBadge>{{ f.type }}</AppBadge></td>
            </tr>
            <tr v-if="!fields.length"><td colspan="2" class="px-4 py-4 text-slate-500">{{ t('data.collection.noFields') }}</td></tr>
          </tbody>
        </table>
      </AppCard>
      <div class="space-y-5 xl:col-span-2">
        <SchemaVersionsPanel :realm="realm" :database="database" :collection="collection" />
        <CollectionDefinition :realm="realm" :database="database" :collection="collection" only="indexes" />
      </div>
    </div>
  </template>
  <CollectionTriggers v-if="tab === 'triggers'" :realm="realm" :database="database" :collection="collection" />
  <CollectionApi v-if="tab === 'api'" :realm="realm" :database="database" :collection="collection" />
  <CollectionDefinition v-if="tab === 'rules'" :realm="realm" :database="database" :collection="collection" only="rules" />

  <template v-if="tab === 'records'">
  <div class="flex flex-wrap items-center gap-2 rounded-t-lg border border-b-0 border-slate-200 bg-white px-3 py-2 dark:border-slate-800 dark:bg-slate-900" role="toolbar" :aria-label="t('data.collection.where')">
    <AppButton variant="secondary" :aria-expanded="filterOpen" data-testid="filter-toggle" @click="filterOpen = !filterOpen">
      <PhFunnel aria-hidden="true" /> {{ t('data.collection.filter') }}<span v-if="activeFilters" class="ml-1 rounded-full bg-brand-700 px-1.5 text-xs text-white">{{ activeFilters }}</span>
    </AppButton>
    <label class="sr-only" for="sort-field">{{ t('data.collection.orderBy') }}</label>
    <select id="sort-field" v-model="sortField" class="h-9 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-slate-700 dark:bg-slate-900" @change="apply">
      <option value="">{{ t('data.collection.default') }}</option>
      <option v-for="f in fields" :key="f.path" :value="f.path">{{ f.path }}</option>
    </select>
    <label class="sr-only" for="sort-dir">{{ t('data.collection.direction') }}</label>
    <select id="sort-dir" v-model="sortDir" class="h-9 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-slate-700 dark:bg-slate-900" @change="apply">
      <option value="asc">{{ t('data.collection.asc') }}</option>
      <option value="desc">{{ t('data.collection.desc') }}</option>
    </select>
    <label v-if="softDelete" class="flex items-center gap-1.5 text-xs text-slate-600 dark:text-slate-400"><input type="checkbox" :checked="view === 'trash'" data-testid="show-deleted" @change="switchView(view === 'trash' ? 'live' : 'trash')" /> {{ t('data.collection.trash') }}</label>
    <span class="ml-auto text-xs text-slate-500" data-testid="total">{{ range }}</span>
    <label class="sr-only" for="page-size">{{ t('data.collection.pageSize') }}</label>
    <select id="page-size" v-model.number="limit" class="h-9 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-slate-700 dark:bg-slate-900" @change="apply">
      <option v-for="n in [20, 50, 100]" :key="n" :value="n">{{ n }}</option>
    </select>
    <button type="button" class="grid size-8 place-items-center rounded-md hover:bg-slate-100 disabled:opacity-40 dark:hover:bg-slate-800" :disabled="pageIndex === 0 || loading" :aria-label="t('common.previous')" @click="page(-1)"><PhCaretLeft aria-hidden="true" /></button>
    <button type="button" class="grid size-8 place-items-center rounded-md hover:bg-slate-100 disabled:opacity-40 dark:hover:bg-slate-800" :disabled="!hasMore || loading" :aria-label="t('common.next')" @click="page(1)"><PhCaretRight aria-hidden="true" /></button>
    <div class="flex overflow-hidden rounded-md border border-slate-300 text-xs dark:border-slate-700" role="group" :aria-label="t('data.collection.view')">
      <button type="button" class="px-2.5 py-1.5" :class="!asJson ? 'bg-slate-200 dark:bg-slate-700' : ''" :aria-pressed="!asJson" @click="asJson = false">{{ t('data.collection.table') }}</button>
      <button type="button" class="px-2.5 py-1.5" :class="asJson ? 'bg-slate-200 dark:bg-slate-700' : ''" :aria-pressed="asJson" @click="asJson = true">{{ t('data.collection.json') }}</button>
    </div>
  </div>

  <form v-if="filterOpen" class="space-y-3 border border-b-0 border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-900/60" role="search" :aria-label="t('data.collection.where')" @submit.prevent="apply">
    <template v-if="!rawOpen">
      <div v-for="(c, i) in conditions" :key="i" class="flex flex-wrap items-end gap-2" data-testid="condition">
        <div class="space-y-1">
          <label :for="`cf-${i}`" class="block text-xs font-medium">{{ t('data.collection.field') }}</label>
          <select :id="`cf-${i}`" v-model="c.field" class="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900" @change="onFieldChange(c)">
            <option v-for="f in fields" :key="f.path" :value="f.path">{{ f.path }}</option>
          </select>
        </div>
        <div class="space-y-1">
          <label :for="`co-${i}`" class="block text-xs font-medium">{{ t('data.collection.operator') }}</label>
          <select :id="`co-${i}`" v-model="c.op" class="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900" @change="c.value = ''">
            <option v-for="op in operatorsFor(fieldOf(c)?.type ?? 'string')" :key="op" :value="op">{{ t(`data.collection.opNames.${op}`) }}</option>
          </select>
        </div>
        <div class="min-w-48 flex-1 space-y-1">
          <label :for="`cv-${i}`" class="block text-xs font-medium">{{ t('data.collection.value') }}</label>
          <select v-if="valueKind(c) === 'bool'" :id="`cv-${i}`" v-model="c.value" class="block w-full rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900">
            <option value="" disabled>—</option>
            <option value="true">{{ t('common.yes') }}</option>
            <option value="false">{{ t('common.no') }}</option>
          </select>
          <select v-else-if="valueKind(c) === 'enum'" :id="`cv-${i}`" v-model="c.value" class="block w-full rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900">
            <option value="" disabled>—</option>
            <option v-for="o in fieldOf(c)?.options ?? []" :key="String(o)" :value="String(o)">{{ String(o) }}</option>
          </select>
          <input v-else :id="`cv-${i}`" v-model="c.value" :type="valueKind(c) === 'date' ? 'datetime-local' : 'text'" autocomplete="off" class="block w-full rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900" />
        </div>
        <button type="button" class="pb-1.5 text-sm underline" @click="conditions.splice(i, 1)">{{ t('data.collection.removeCondition') }}</button>
      </div>
      <AppButton variant="secondary" @click="addCondition">{{ t('data.collection.addCondition') }}</AppButton>
      <p v-if="conditions.length" class="text-xs text-slate-600 dark:text-slate-400">{{ t('data.collection.valueHelp') }}</p>
    </template>
    <div v-else class="space-y-1">
      <label for="raw-where" class="block text-sm font-medium">{{ t('data.collection.raw') }}</label>
      <textarea id="raw-where" v-model="raw" rows="4" spellcheck="false" aria-describedby="raw-help" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-xs dark:border-slate-600 dark:bg-slate-900" />
      <p id="raw-help" class="text-xs text-slate-600 dark:text-slate-400">{{ t('data.collection.rawHelp') }}</p>
    </div>
    <div class="flex flex-wrap items-center gap-3">
      <label class="flex items-center gap-2 text-sm"><input v-model="rawOpen" type="checkbox" /> {{ t('data.collection.rawToggle') }}</label>
      <AppButton type="submit" :busy="loading">{{ t('data.collection.apply') }}</AppButton>
      <AppButton variant="secondary" @click="clear">{{ t('data.collection.clear') }}</AppButton>
    </div>
  </form>

  <AppAlert v-if="queryProblem" kind="error" class="my-3">{{ queryProblem }}</AppAlert>
  <AppAlert v-if="error" kind="error" class="my-3">{{ error }}</AppAlert>

  <div v-if="!asJson" class="overflow-x-auto rounded-b-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="docs-table">
      <thead>
        <tr>
          <th scope="col" class="px-3 py-2">id</th>
          <th v-for="col in columns" :key="col" scope="col" class="px-3 py-2">{{ col }}</th>
          <th scope="col" class="px-3 py-2">{{ view === 'trash' ? t('data.collection.deleted') : t('data.collection.updated') }}</th>
          <th scope="col" class="px-3 py-2">{{ view === 'trash' ? t('data.collection.purgeAt') : t('data.collection.version') }}</th>
          <th v-if="view === 'trash' && canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('data.collection.restore') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="d in docs" :key="d.id" class="border-t border-slate-100 dark:border-slate-800">
          <td class="px-3 py-2 font-mono text-xs">
            <RouterLink :to="{ name: 'data-doc', params: { realm, database, collection, id: d.id } }" class="text-brand-700 hover:underline dark:text-brand-400">{{ d.id }}</RouterLink>
          </td>
          <td v-for="col in columns" :key="col" class="px-3 py-2">{{ cell(d, col) }}</td>
          <td class="px-3 py-2 whitespace-nowrap text-slate-600 dark:text-slate-400">{{ formatDate(view === 'trash' ? d._meta.deleted_at : d._meta.updated_at) }}</td>
          <td class="px-3 py-2 whitespace-nowrap text-slate-600 dark:text-slate-400">{{ view === 'trash' ? formatDate(d._meta.purge_at) : (d._meta.version ?? '—') }}</td>
          <td v-if="view === 'trash' && canWrite" class="px-3 py-2 text-right">
            <button type="button" class="text-brand-700 underline dark:text-brand-400" :aria-label="`${t('data.collection.restore')} ${d.id}`" @click="restore(d)">{{ t('data.collection.restore') }}</button>
          </td>
        </tr>
        <tr v-if="!docs.length && !loading">
          <td :colspan="columns.length + 4" class="px-3 py-8 text-center text-slate-600 dark:text-slate-400">{{ t('data.collection.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <div v-else><JsonView :value="docs" :label="t('data.collection.json')" /></div>
  </template>

  <FormDialog v-model:open="assigning" :title="t('data.collection.assign.title')" :description="t('data.collection.assign.help')" :submit="t('data.collection.assign.action')" :busy="assignBusy" :error="assignError" @submit="assign">
    <div class="space-y-1">
      <label for="assign-org" class="block text-sm font-medium">{{ t('data.doc.org') }}</label>
      <select id="assign-org" v-model="assignTo" required class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="" disabled>—</option>
        <option v-for="o in assignOrgs" :key="o.id" :value="o.id">{{ o.name }}</option>
      </select>
    </div>
    <p v-if="assignMatched !== null" class="text-sm" role="status" data-testid="assign-matched">{{ t('data.collection.assign.matched', { n: assignMatched }) }}</p>
  </FormDialog>
</template>
