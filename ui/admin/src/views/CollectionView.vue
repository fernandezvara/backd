<script setup lang="ts">
import type { Doc } from 'backd-js'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import JsonView from '@/components/JsonView.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { buildWhere, operatorsFor, orderBy, queryFields, QueryError, type Condition } from '@/lib/query'
import { resolve, type Schema } from '@/lib/schema'
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
    docs.value = page.items
    hasMore.value = page.has_more
    total.value = page.total
    nextStart = page.has_more ? (page.next_cursor ? { after: page.next_cursor, skip: 0 } : { skip: start.skip + limit.value }) : undefined
  } catch (e) {
    error.value = errorText(e, t('data.collection.loadFailed'))
  } finally {
    loading.value = false
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
const showStatus = computed(() => (total.value !== undefined ? t('data.collection.total', { n: total.value }) : ''))
</script>

<template>
  <nav class="text-sm" aria-label="Breadcrumb">
    <RouterLink :to="{ name: 'data', params: { realm } }" class="underline">{{ t('nav.data') }}</RouterLink>
    <span aria-hidden="true"> / </span><span class="font-mono">{{ database }}</span>
  </nav>
  <div class="mt-2 flex flex-wrap items-end justify-between gap-4">
    <h1 class="font-mono text-2xl font-semibold">{{ collection }}</h1>
    <div class="flex flex-wrap items-center gap-2">
      <div v-if="softDelete" class="flex overflow-hidden rounded-md border border-slate-300 text-sm dark:border-slate-600" role="group" :aria-label="t('data.collection.view')">
        <button type="button" class="px-3 py-1.5" :class="view === 'live' ? 'bg-brand-700 text-white' : ''" :aria-pressed="view === 'live'" @click="switchView('live')">{{ t('data.collection.live') }}</button>
        <button type="button" class="px-3 py-1.5" :class="view === 'trash' ? 'bg-brand-700 text-white' : ''" :aria-pressed="view === 'trash'" @click="switchView('trash')">{{ t('data.collection.trash') }}</button>
      </div>
      <RouterLink v-if="canWrite" :to="{ name: 'data-new', params: { realm, database, collection } }" class="rounded-md bg-brand-700 px-3.5 py-2 text-sm font-medium text-white hover:bg-brand-800">{{ t('data.collection.new') }}</RouterLink>
    </div>
  </div>

  <form class="mt-4 space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" role="search" :aria-label="t('data.collection.where')" @submit.prevent="apply">
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

    <div class="flex flex-wrap items-end gap-3">
      <div class="space-y-1">
        <label for="sort-field" class="block text-xs font-medium">{{ t('data.collection.orderBy') }}</label>
        <select id="sort-field" v-model="sortField" class="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900">
          <option value="">{{ t('data.collection.default') }}</option>
          <option v-for="f in fields" :key="f.path" :value="f.path">{{ f.path }}</option>
        </select>
      </div>
      <div class="space-y-1">
        <label for="sort-dir" class="block text-xs font-medium">{{ t('data.collection.direction') }}</label>
        <select id="sort-dir" v-model="sortDir" class="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900">
          <option value="asc">{{ t('data.collection.asc') }}</option>
          <option value="desc">{{ t('data.collection.desc') }}</option>
        </select>
      </div>
      <div class="space-y-1">
        <label for="page-size" class="block text-xs font-medium">{{ t('data.collection.pageSize') }}</label>
        <select id="page-size" v-model.number="limit" class="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-600 dark:bg-slate-900">
          <option v-for="n in [20, 50, 100]" :key="n" :value="n">{{ n }}</option>
        </select>
      </div>
      <label class="flex items-center gap-2 pb-1.5 text-sm"><input v-model="rawOpen" type="checkbox" /> {{ t('data.collection.rawToggle') }}</label>
      <AppButton type="submit" :busy="loading">{{ t('data.collection.apply') }}</AppButton>
      <AppButton variant="secondary" @click="clear">{{ t('data.collection.clear') }}</AppButton>
    </div>
  </form>

  <AppAlert v-if="queryProblem" kind="error" class="mt-4">{{ queryProblem }}</AppAlert>
  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <div class="mt-4 flex items-center justify-between text-sm">
    <span data-testid="total" class="text-slate-600 dark:text-slate-400">{{ showStatus }}</span>
    <div class="flex overflow-hidden rounded-md border border-slate-300 dark:border-slate-600" role="group" :aria-label="t('data.collection.view')">
      <button type="button" class="px-3 py-1" :class="!asJson ? 'bg-slate-200 dark:bg-slate-700' : ''" :aria-pressed="!asJson" @click="asJson = false">{{ t('data.collection.table') }}</button>
      <button type="button" class="px-3 py-1" :class="asJson ? 'bg-slate-200 dark:bg-slate-700' : ''" :aria-pressed="asJson" @click="asJson = true">{{ t('data.collection.json') }}</button>
    </div>
  </div>

  <div v-if="!asJson" class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="docs-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">id</th>
          <th v-for="col in columns" :key="col" scope="col" class="px-3 py-2">{{ col }}</th>
          <th scope="col" class="px-3 py-2">{{ view === 'trash' ? t('data.collection.deleted') : t('data.collection.updated') }}</th>
          <th scope="col" class="px-3 py-2">{{ view === 'trash' ? t('data.collection.purgeAt') : t('data.collection.version') }}</th>
          <th v-if="view === 'trash' && canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('data.collection.restore') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="d in docs" :key="d.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 font-mono text-xs">
            <RouterLink :to="{ name: 'data-doc', params: { realm, database, collection, id: d.id } }" class="underline">{{ d.id }}</RouterLink>
          </td>
          <td v-for="col in columns" :key="col" class="px-3 py-2">{{ cell(d, col) }}</td>
          <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(view === 'trash' ? d._meta.deleted_at : d._meta.updated_at) }}</td>
          <td class="px-3 py-2 whitespace-nowrap">{{ view === 'trash' ? formatDate(d._meta.purge_at) : (d._meta.version ?? '—') }}</td>
          <td v-if="view === 'trash' && canWrite" class="px-3 py-2 text-right">
            <button type="button" class="underline" :aria-label="`${t('data.collection.restore')} ${d.id}`" @click="restore(d)">{{ t('data.collection.restore') }}</button>
          </td>
        </tr>
        <tr v-if="!docs.length && !loading">
          <td :colspan="columns.length + 4" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('data.collection.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <div v-else class="mt-2"><JsonView :value="docs" :label="t('data.collection.json')" /></div>

  <div class="mt-3 flex items-center gap-3">
    <AppButton variant="secondary" :disabled="pageIndex === 0 || loading" @click="page(-1)">{{ t('common.previous') }}</AppButton>
    <AppButton variant="secondary" :disabled="!hasMore || loading" @click="page(1)">{{ t('common.next') }}</AppButton>
  </div>
</template>
