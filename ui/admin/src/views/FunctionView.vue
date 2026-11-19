<script setup lang="ts">
import { formatBytes, Job } from 'backd-js'
import type { FunctionSource, InvocationRecord } from 'backd-js'
import { PhArrowLeft } from '@phosphor-icons/vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import FunctionCode from '@/components/FunctionCode.vue'
import SubTabs from '@/components/SubTabs.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText, formatDate, prettyDuration } from '@/lib/format'
import { highlight } from '@/lib/highlight'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string; database: string; name: string }>()
const { t } = useI18n()
const admin = useAdmin()
const raw = useRawAdmin()
const session = useSession()
const toasts = useToasts()
const realmConfig = useRealmConfig()

const canInvoke = computed(() => session.canWrite('functions'))
const tab = ref('code')
const id = computed(() => `${props.database}/${props.name}`)

// What function.yaml says, as GET /_admin/config shows it.
interface FnConfig {
  name: string
  mode: string
  internal: boolean
  admin: boolean
  email: boolean
  dev_only: boolean
  timeout: string
  memory: number
  max_output: number
  concurrency: number
  idempotency: string
  schedule: string
  timezone: string
  overlap: string
  calls: string[]
  secrets: string[]
  network: string[]
  invoke?: string
  rate_limit?: { per: string; limit: number; window: string }
  on_complete?: { function: string; on: string[] }
  retry?: { attempts: number; backoff: string; max_backoff: string }
}
const dbs = computed(() => (realmConfig.config?.databases ?? {}) as Record<string, { functions?: FnConfig[] }>)
const fn = computed(() => dbs.value[props.database]?.functions?.find((f) => f.name === props.name))
const list = (xs: string[]) => (xs.length ? xs.join(', ') : t('functions.noneValue'))

// The source: the header says whether what runs is what the files say.
const source = ref<FunctionSource>()
const bundle = computed(() => (source.value ? (source.value.bundle ? (source.value.bundle.current ? 'current' : 'stale') : 'none') : ''))
const yaml = computed(() => source.value?.files.find((f) => f.role === 'config')?.content ?? '')
const yamlTokens = computed(() => highlight(yaml.value, 'yaml'))

// Invocations
const runs = ref<InvocationRecord[]>([])
const runsError = ref('')
const runsLoaded = ref(false)
async function loadRuns() {
  runsError.value = ''
  try {
    runs.value = (await admin.invocations.list({ function: id.value, limit: 25 })).items
  } catch (e) {
    runsError.value = errorText(e, t('history.loadFailed'))
  } finally {
    runsLoaded.value = true
  }
}
watch(tab, (v) => {
  if (v === 'invocations' && !runsLoaded.value) void loadRuns()
  if (v === 'usedby' && !triggersLoaded.value) void loadTriggers()
})

// Used by: found in the loaded configuration (other functions' calls and on_complete, schedule,
// triggers), not by reading code.
interface TriggerRow {
  database: string
  collection: string
  name: string
  events: string[]
  function: string
}
const triggers = ref<TriggerRow[]>([])
const triggersLoaded = ref(false)
async function loadTriggers() {
  try {
    triggers.value = (await raw<{ triggers: TriggerRow[] }>('GET', ['triggers', 'status'])).triggers
  } catch {
    triggers.value = [] // a level that can't read it just sees none
  } finally {
    triggersLoaded.value = true
  }
}
interface UsedBy {
  kind: string
  where: string
  detail: string
  to?: { name: string; params: Record<string, string> }
}
const usedBy = computed<UsedBy[]>(() => {
  const out: UsedBy[] = []
  const f = fn.value
  if (f && !f.internal) out.push({ kind: t('fn.usedBy.http'), where: `${props.database}/_func/${props.name}`, detail: `POST /v1/${props.realm}/${props.database}/_func/${props.name}` })
  if (f?.schedule) out.push({ kind: t('fn.usedBy.schedule'), where: f.schedule, detail: f.timezone })
  for (const other of dbs.value[props.database]?.functions ?? []) {
    if (other.calls.includes(props.name)) out.push({ kind: t('fn.usedBy.calls'), where: other.name, detail: 'ctx.call', to: { name: 'function', params: { realm: props.realm, database: props.database, name: other.name } } })
    if (other.on_complete?.function === props.name || other.on_complete?.function === id.value) out.push({ kind: t('fn.usedBy.onComplete'), where: other.name, detail: other.on_complete.on.join(', ') || t('fn.usedBy.anyEnd'), to: { name: 'function', params: { realm: props.realm, database: props.database, name: other.name } } })
  }
  for (const tr of triggers.value) {
    if (tr.function === id.value || tr.function === props.name) out.push({ kind: t('fn.usedBy.trigger'), where: `${tr.database}/${tr.collection}`, detail: tr.events.join(', ') })
  }
  return out
})

// Test run
const open = ref(false)
const input = ref('')
const runAs = ref('')
const error = ref('')
const busy = ref(false)
const last = ref<{ output?: string; job?: string }>()
function show() {
  input.value = ''
  runAs.value = ''
  error.value = ''
  open.value = true
}
async function run() {
  let parsed: unknown = null
  if (input.value.trim()) {
    try {
      parsed = JSON.parse(input.value)
    } catch {
      error.value = t('functions.invoke.invalidJson')
      return
    }
  }
  busy.value = true
  error.value = ''
  try {
    const out = await admin.invokeFunction(id.value, { input: parsed, as: runAs.value.trim() || undefined })
    open.value = false
    last.value = out instanceof Job ? { job: out.id } : { output: JSON.stringify(out, null, 2) }
    toasts.push(t('functions.invoke.outputTitle', { function: id.value }))
    runsLoaded.value = false
    if (tab.value === 'invocations') void loadRuns()
  } catch (e) {
    error.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}

onMounted(() => void realmConfig.load())
const statusClass = (s: string) => (s === 'ok' ? '' : 'font-semibold text-red-800 dark:text-red-300')
</script>

<template>
  <RouterLink :to="{ name: 'functions', params: { realm } }" class="mb-3 inline-flex items-center gap-1.5 text-sm font-medium hover:underline" data-testid="back-to-functions">
    <PhArrowLeft :size="14" aria-hidden="true" />{{ t('fn.back') }}
  </RouterLink>

  <div class="mb-4 flex flex-wrap items-center gap-3">
    <div class="min-w-0">
      <h1 class="font-mono text-xl font-semibold" data-testid="function-name">{{ name }}</h1>
      <p class="text-sm text-slate-600 dark:text-slate-400">
        {{ database }}
        <template v-if="source"> · Deno {{ source.deno }} · {{ t('fn.sourceHash') }} <span class="font-mono">{{ source.source_hash.slice(0, 8) }}</span></template>
      </p>
      <div class="mt-1.5 flex flex-wrap gap-1.5">
        <AppBadge v-if="bundle === 'current'" kind="ok">{{ t('fn.bundle.current') }}</AppBadge>
        <AppBadge v-else-if="bundle === 'stale'" kind="warn" :title="t('fn.bundle.staleHelp')">{{ t('fn.bundle.stale') }}</AppBadge>
        <AppBadge v-else-if="bundle === 'none'" kind="warn">{{ t('fn.bundle.none') }}</AppBadge>
        <template v-if="fn">
          <AppBadge kind="blue">{{ t(`functions.modes.${fn.mode}`) }}</AppBadge>
          <AppBadge v-if="fn.internal" kind="warn">{{ t('functions.internal') }}</AppBadge>
          <AppBadge v-if="fn.admin">{{ t('functions.admin') }}</AppBadge>
          <AppBadge v-if="fn.email">{{ t('functions.email') }}</AppBadge>
        </template>
      </div>
    </div>
    <div v-if="canInvoke" class="ml-auto"><AppButton @click="show">{{ t('fn.testRun') }}</AppButton></div>
  </div>

  <AppAlert v-if="last" class="mb-4" data-testid="last-run">
    <p class="font-medium">{{ t('functions.invoke.outputTitle', { function: id }) }}</p>
    <pre v-if="last.output !== undefined" class="mt-2 max-h-64 overflow-auto rounded bg-slate-100 p-2 text-xs dark:bg-slate-800" tabindex="0">{{ last.output }}</pre>
    <p v-else class="mt-1">{{ t('functions.invoke.job', { id: last.job }) }}</p>
  </AppAlert>
  <AppAlert v-if="realmConfig.config && !fn" kind="error" class="mb-4">{{ t('fn.notFound') }}</AppAlert>

  <SubTabs v-model="tab" :label="t('fn.tabs.label')" :tabs="[{ id: 'code', label: t('fn.tabs.code') }, { id: 'invocations', label: t('fn.tabs.invocations') }, { id: 'settings', label: t('fn.tabs.settings') }, { id: 'usedby', label: t('fn.tabs.usedBy') }]" />

  <div v-show="tab === 'code'"><FunctionCode :database="database" :name="name" @loaded="source = $event" /></div>

  <div v-if="tab === 'invocations'">
    <AppAlert v-if="runsError" kind="error">{{ runsError }}</AppAlert>
    <AppCard v-else flush>
      <table class="w-full text-left text-sm" data-testid="fn-runs">
        <thead>
          <tr>
            <th scope="col" class="px-3 py-2">{{ t('history.time') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('history.status') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('history.duration') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('history.origin') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('history.actor') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('history.requestId') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in runs" :key="r.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
            <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(r.at) }}</td>
            <td class="px-3 py-2" :class="statusClass(r.status)">{{ r.status }}<span v-if="r.code" class="font-mono text-xs"> · {{ r.code }}</span></td>
            <td class="px-3 py-2 whitespace-nowrap">{{ t('history.ms', { n: r.duration_ms }) }}</td>
            <td class="px-3 py-2 font-mono text-xs">{{ r.origin ?? '—' }}</td>
            <td class="px-3 py-2 font-mono text-xs break-all">{{ r.actor }}</td>
            <td class="px-3 py-2 font-mono text-xs">{{ r.request_id ?? '—' }}</td>
          </tr>
          <tr v-if="!runs.length && runsLoaded">
            <td colspan="6" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('history.empty') }}</td>
          </tr>
        </tbody>
      </table>
      <p class="border-t border-slate-200 p-3 text-xs dark:border-slate-800">
        <RouterLink :to="{ name: 'functions-history', params: { realm }, query: { function: id } }" class="underline">{{ t('fn.seeAllRuns') }}</RouterLink>
      </p>
    </AppCard>
  </div>

  <div v-show="tab === 'settings'" class="grid gap-5 lg:grid-cols-3">
    <AppCard flush class="lg:col-span-2">
      <h2 class="border-b border-slate-200 px-4 py-2.5 font-mono text-sm font-semibold dark:border-slate-800">function.yaml</h2>
      <pre v-if="yaml" class="overflow-auto px-4 py-3 font-mono text-[12.5px] leading-relaxed" tabindex="0"><span v-for="(tk, i) in yamlTokens" :key="i" :class="tk.type ? `hl-${tk.type}` : undefined">{{ tk.text }}</span></pre>
      <p v-else class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('fn.noYaml') }}</p>
    </AppCard>
    <AppCard v-if="fn" flush class="h-fit">
      <h2 class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('fn.effective') }}</h2>
      <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.timeout') }}</dt><dd>{{ prettyDuration(fn.timeout) }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.memory') }}</dt><dd>{{ formatBytes(fn.memory) }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.maxOutput') }}</dt><dd>{{ formatBytes(fn.max_output) }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.concurrency') }}</dt><dd>{{ fn.concurrency }}</dd></div>
        <div v-if="fn.idempotency" class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('fn.idempotency') }}</dt><dd>{{ fn.idempotency }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.secrets') }}</dt><dd class="font-mono text-xs">{{ list(fn.secrets) }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.network') }}</dt><dd class="font-mono text-xs">{{ list(fn.network) }}</dd></div>
        <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.calls') }}</dt><dd class="font-mono text-xs">{{ list(fn.calls) }}</dd></div>
        <div v-if="fn.invoke" class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.invokeRule') }}</dt><dd class="font-mono text-xs">{{ fn.invoke }}</dd></div>
        <div v-if="fn.retry" class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.retry') }}</dt><dd>{{ t('functions.retryText', { attempts: fn.retry.attempts, backoff: prettyDuration(fn.retry.backoff) }) }}</dd></div>
        <div v-if="fn.rate_limit" class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('functions.rateLimit') }}</dt><dd>{{ t('functions.rateText', { limit: fn.rate_limit.limit, per: fn.rate_limit.per, window: prettyDuration(fn.rate_limit.window) }) }}</dd></div>
      </dl>
    </AppCard>
  </div>

  <div v-if="tab === 'usedby'">
    <AppCard flush>
      <table class="w-full text-left text-sm" data-testid="fn-usedby">
        <thead>
          <tr>
            <th scope="col" class="px-4 py-2">{{ t('fn.usedBy.where') }}</th>
            <th scope="col" class="px-4 py-2">{{ t('fn.usedBy.kind') }}</th>
            <th scope="col" class="px-4 py-2">{{ t('fn.usedBy.detail') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(u, i) in usedBy" :key="i" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
            <td class="px-4 py-2 font-mono text-xs"><RouterLink v-if="u.to" :to="u.to" class="hover:underline">{{ u.where }}</RouterLink><template v-else>{{ u.where }}</template></td>
            <td class="px-4 py-2">{{ u.kind }}</td>
            <td class="px-4 py-2 font-mono text-xs text-slate-600 dark:text-slate-400">{{ u.detail }}</td>
          </tr>
          <tr v-if="!usedBy.length">
            <td colspan="3" class="px-4 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('fn.usedBy.none') }}</td>
          </tr>
        </tbody>
      </table>
      <p class="border-t border-slate-200 p-3 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-400">{{ t('fn.usedBy.note') }}</p>
    </AppCard>
  </div>

  <FormDialog v-model:open="open" :title="t('fn.testRun')" :description="t('functions.invoke.help')" :submit="busy ? t('functions.invoke.running') : t('functions.invoke.submit')" :busy="busy" :error="error" @submit="run">
    <div class="space-y-1">
      <label for="fn-input" class="block text-sm font-medium">{{ t('functions.invoke.input') }}</label>
      <textarea id="fn-input" v-model="input" rows="5" spellcheck="false" aria-describedby="fn-input-help" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-xs dark:border-slate-600 dark:bg-slate-900" />
      <p id="fn-input-help" class="text-xs text-slate-600 dark:text-slate-400">{{ t('functions.invoke.inputHelp') }}</p>
    </div>
    <TextField v-model="runAs" type="email" :label="t('functions.invoke.as')" :help="t('functions.invoke.asHelp')" autocomplete="off" />
  </FormDialog>
</template>
