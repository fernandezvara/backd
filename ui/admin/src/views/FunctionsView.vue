<script setup lang="ts">
import { formatBytes, Job } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import { PhCaretDown, PhCaretRight, PhMagnifyingGlass } from '@phosphor-icons/vue'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import AppButton from '@/components/AppButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import FormDialog from '@/components/FormDialog.vue'
import FunctionsTabs from '@/components/FunctionsTabs.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { downloadText } from '@/lib/draft'
import { errorText, prettyDuration } from '@/lib/format'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// What function.yaml says, as GET /_admin/config shows it.
interface FunctionConfig {
  name: string
  file: string
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

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const realmConfig = useRealmConfig()

const canConfig = computed(() => session.canRead('config'))
const canInvoke = computed(() => session.canWrite('functions'))
const loadError = ref('')
// Which schedules are paused, by "<database>/<name>": runtime state, not config.
const paused = ref<Record<string, boolean>>({})
const pausing = ref('')
async function loadSchedules() {
  try {
    paused.value = Object.fromEntries((await admin.schedules.list()).map((s) => [s.function, s.paused]))
  } catch {
    paused.value = {} // the config still shows; only the paused state is missing
  }
}
onMounted(async () => {
  await Promise.all([realmConfig.load(), loadSchedules()])
  if (canConfig.value && !realmConfig.config) loadError.value = t('functions.loadFailed')
})
// The realm's OpenAPI documents as files: the client one (what applications reach, no admin
// routes) and the admin one (everything). The routes need the bearer token, so a plain link
// can't fetch them.
const downloading = ref(false)
async function downloadOpenAPI(client: boolean) {
  downloading.value = true
  try {
    const doc = client ? await admin.clientOpenapi() : await admin.openapi()
    downloadText(`${props.realm}-openapi${client ? '-client' : '-admin'}.json`, JSON.stringify(doc, null, 2), 'application/json')
    toasts.push(t('functions.openapi.done'))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    downloading.value = false
  }
}
async function togglePause(name: string) {
  pausing.value = name
  try {
    const s = paused.value[name] ? await admin.schedules.resume(name) : await admin.schedules.pause(name)
    paused.value = { ...paused.value, [name]: s.paused }
    toasts.push(t(s.paused ? 'functions.pause.done' : 'functions.resume.done', { function: name }))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    pausing.value = ''
  }
}

const databases = computed(() => {
  const dbs = (realmConfig.config?.databases ?? {}) as Record<string, { functions?: FunctionConfig[] }>
  return Object.entries(dbs)
    .map(([name, db]) => ({ name, functions: db.functions ?? [] }))
    .filter((db) => db.functions.length)
    .sort((a, b) => a.name.localeCompare(b.name))
})
// One row per function, over every database; the filters work on it.
const rows = computed(() => databases.value.flatMap((db) => db.functions.map((f) => ({ id: `${db.name}/${f.name}`, db: db.name, f }))))
const filter = ref('')
const modeFilter = ref('')
const modes = computed(() => [...new Set(rows.value.map((r) => r.f.mode))].sort())
const shown = computed(() =>
  rows.value.filter((r) => {
    if (filter.value && !r.id.toLowerCase().includes(filter.value.trim().toLowerCase())) return false
    const m = modeFilter.value
    if (!m) return true
    return m === 'scheduled' ? !!r.f.schedule : m === 'internal' ? r.f.internal : r.f.mode === m
  }),
)
const opened = ref(new Set<string>())
function toggle(id: string) {
  const next = new Set(opened.value)
  if (!next.delete(id)) next.add(id)
  opened.value = next
}
const allFunctions = computed(() => databases.value.flatMap((db) => db.functions.map((f) => `${db.name}/${f.name}`)))
const list = (xs: string[]) => (xs.length ? xs.join(', ') : t('functions.noneValue'))

// Run by hand.
const open = ref(false)
const fn = ref('')
const input = ref('')
const runAs = ref('')
const key = ref('')
const error = ref('')
const busy = ref(false)
const last = ref<{ function: string; output?: string; job?: string }>()

function show(name = '') {
  fn.value = name
  input.value = ''
  runAs.value = ''
  key.value = ''
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
    const out = await admin.invokeFunction(fn.value.trim(), { input: parsed, as: runAs.value.trim() || undefined, idempotencyKey: key.value.trim() || undefined })
    open.value = false
    last.value = out instanceof Job ? { function: fn.value.trim(), job: out.id } : { function: fn.value.trim(), output: JSON.stringify(out, null, 2) }
    toasts.push(t('functions.invoke.outputTitle', { function: fn.value.trim() }))
  } catch (e) {
    error.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader :title="t('functions.title')" :description="t('nav.desc.functions')">
    <AppButton v-if="canConfig" variant="secondary" :busy="downloading" data-testid="download-openapi-client" :title="t('functions.openapi.clientHelp')" @click="downloadOpenAPI(true)">{{ t('functions.openapi.client') }}</AppButton>
    <AppButton v-if="canConfig" variant="secondary" :busy="downloading" data-testid="download-openapi-admin" :title="t('functions.openapi.adminHelp')" @click="downloadOpenAPI(false)">{{ t('functions.openapi.admin') }}</AppButton>
    <AppButton v-if="canInvoke" @click="show()">{{ t('functions.invoke.open') }}</AppButton>
  </PageHeader>
  <FunctionsTabs />

  <AppAlert v-if="last" class="mb-4" data-testid="last-run">
    <p class="font-medium">{{ t('functions.invoke.outputTitle', { function: last.function }) }}</p>
    <pre v-if="last.output !== undefined" class="mt-2 max-h-64 overflow-auto rounded bg-slate-100 p-2 text-xs dark:bg-slate-800" tabindex="0" data-testid="last-output">{{ last.output }}</pre>
    <p v-else class="mt-1">
      {{ t('functions.invoke.job', { id: last.job }) }}
      <RouterLink :to="{ name: 'functions-jobs', params: { realm }, query: { function: last.function } }" class="ml-1 underline">{{ t('functions.invoke.seeJob') }}</RouterLink>
    </p>
  </AppAlert>

  <AppAlert v-if="!canConfig">{{ t('functions.needConfig') }}</AppAlert>
  <AppAlert v-else-if="loadError" kind="error">{{ loadError }}</AppAlert>
  <p v-else-if="realmConfig.config && !databases.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('functions.none') }}</p>

  <AppCard v-if="rows.length || filter || modeFilter" flush>
    <div class="flex flex-wrap items-center gap-2 border-b border-slate-200 p-3 dark:border-slate-800">
      <div class="relative">
        <PhMagnifyingGlass :size="14" class="pointer-events-none absolute left-2.5 top-2.5 text-slate-400" aria-hidden="true" />
        <input v-model="filter" type="search" class="h-8 w-72 max-w-full rounded-md border border-slate-300 bg-white pl-8 pr-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :placeholder="t('functions.filter')" :aria-label="t('functions.filter')" autocomplete="off" />
      </div>
      <select v-model="modeFilter" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('functions.kind')">
        <option value="">{{ t('functions.allKinds') }}</option>
        <option v-for="m in modes" :key="m" :value="m">{{ t(`functions.modes.${m}`) }}</option>
        <option value="scheduled">{{ t('functions.scheduledOnly') }}</option>
        <option value="internal">{{ t('functions.internal') }}</option>
      </select>
      <span class="ml-auto text-xs text-slate-600 dark:text-slate-400">{{ t('functions.count', { n: shown.length }) }}</span>
    </div>
    <table class="w-full text-left text-sm" data-testid="functions-table">
      <thead>
        <tr>
          <th scope="col" class="w-8 px-3 py-2"><span class="sr-only">{{ t('functions.details') }}</span></th>
          <th scope="col" class="px-3 py-2">{{ t('functions.function') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('functions.kind') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('functions.schedule') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('functions.limits') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('functions.uses') }}</th>
          <th v-if="canInvoke" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('functions.invoke.submit') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="r in shown" :key="r.id">
          <tr class="border-b border-slate-100 dark:border-slate-800" :data-testid="`fn-${r.db}-${r.f.name}`">
            <td class="px-3 py-2">
              <button type="button" class="text-slate-500 hover:text-slate-900 dark:hover:text-slate-100" :aria-expanded="opened.has(r.id)" :aria-label="`${t('functions.details')} ${r.f.name}`" @click="toggle(r.id)">
                <component :is="opened.has(r.id) ? PhCaretDown : PhCaretRight" :size="14" aria-hidden="true" />
              </button>
            </td>
            <td class="px-3 py-2">
              <div class="font-mono font-medium">{{ r.f.name }}</div>
              <div class="text-xs text-slate-500">{{ r.db }}</div>
            </td>
            <td class="px-3 py-2">
              <span class="flex flex-wrap gap-1">
                <AppBadge kind="blue">{{ t(`functions.modes.${r.f.mode}`) }}</AppBadge>
                <AppBadge v-if="r.f.internal" kind="warn">{{ t('functions.internal') }}</AppBadge>
                <AppBadge v-if="r.f.admin">{{ t('functions.admin') }}</AppBadge>
                <AppBadge v-if="r.f.email">{{ t('functions.email') }}</AppBadge>
                <AppBadge v-if="r.f.dev_only">{{ t('functions.devOnly') }}</AppBadge>
              </span>
            </td>
            <td class="px-3 py-2">
              <span v-if="!r.f.schedule" class="text-slate-500">—</span>
              <span v-else class="flex flex-wrap items-center gap-1.5">
                <span class="font-mono text-xs">{{ r.f.schedule }}</span>
                <AppBadge v-if="paused[r.id]" kind="warn">{{ t('functions.paused') }}</AppBadge>
                <AppBadge v-else kind="ok">{{ t('functions.active') }}</AppBadge>
              </span>
            </td>
            <td class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ prettyDuration(r.f.timeout) }} · {{ formatBytes(r.f.memory) }}</td>
            <td class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ t('functions.usesText', { calls: r.f.calls.length, secrets: r.f.secrets.length, network: r.f.network.length }) }}</td>
            <td v-if="canInvoke" class="px-3 py-2 text-right">
              <span class="inline-flex gap-2">
                <AppButton v-if="r.f.schedule" variant="secondary" :busy="pausing === r.id" @click="togglePause(r.id)">{{ paused[r.id] ? t('functions.resume.open') : t('functions.pause.open') }}</AppButton>
                <AppButton variant="secondary" @click="show(r.id)">{{ t('functions.invoke.submit') }}</AppButton>
              </span>
            </td>
          </tr>
          <tr v-if="opened.has(r.id)" class="border-b border-slate-100 bg-slate-50 dark:border-slate-800 dark:bg-slate-900/50">
            <td />
            <td :colspan="canInvoke ? 6 : 5" class="px-3 py-3">
              <dl class="grid max-w-3xl grid-cols-[8rem_1fr] gap-x-3 gap-y-1 text-[13px]">
                <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.limits') }}</dt>
                <dd>{{ t('functions.timeout') }} {{ prettyDuration(r.f.timeout) }} · {{ t('functions.memory') }} {{ formatBytes(r.f.memory) }} · {{ t('functions.maxOutput') }} {{ formatBytes(r.f.max_output) }} · {{ t('functions.concurrency') }} {{ r.f.concurrency }}</dd>
                <template v-if="r.f.schedule && (r.f.timezone !== 'UTC' || r.f.overlap === 'skip')">
                  <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.schedule') }}</dt>
                  <dd>{{ r.f.timezone }}<span v-if="r.f.overlap === 'skip'"> · {{ t('functions.overlapSkip') }}</span></dd>
                </template>
                <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.calls') }}</dt>
                <dd class="font-mono text-xs">{{ list(r.f.calls) }}</dd>
                <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.secrets') }}</dt>
                <dd class="font-mono text-xs">{{ list(r.f.secrets) }}</dd>
                <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.network') }}</dt>
                <dd class="font-mono text-xs">{{ list(r.f.network) }}</dd>
                <template v-if="r.f.on_complete">
                  <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.onComplete') }}</dt>
                  <dd class="font-mono text-xs">{{ r.f.on_complete.function }}<span v-if="r.f.on_complete.on.length" class="font-sans text-slate-600 dark:text-slate-400"> · {{ r.f.on_complete.on.join(', ') }}</span></dd>
                </template>
                <template v-if="r.f.retry">
                  <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.retry') }}</dt>
                  <dd>{{ t('functions.retryText', { attempts: r.f.retry.attempts, backoff: prettyDuration(r.f.retry.backoff) }) }}</dd>
                </template>
                <template v-if="r.f.rate_limit">
                  <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.rateLimit') }}</dt>
                  <dd>{{ t('functions.rateText', { limit: r.f.rate_limit.limit, per: r.f.rate_limit.per, window: prettyDuration(r.f.rate_limit.window) }) }}</dd>
                </template>
                <template v-if="r.f.invoke">
                  <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.invokeRule') }}</dt>
                  <dd class="font-mono text-xs">{{ r.f.invoke }}</dd>
                </template>
                <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.fileLabel') }}</dt>
                <dd class="font-mono text-xs">{{ r.f.file }}</dd>
              </dl>
            </td>
          </tr>
        </template>
        <tr v-if="!shown.length">
          <td :colspan="canInvoke ? 7 : 6" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('functions.noMatch') }}</td>
        </tr>
      </tbody>
    </table>
  </AppCard>

  <FormDialog v-model:open="open" :title="t('functions.invoke.title')" :description="t('functions.invoke.help')" :submit="busy ? t('functions.invoke.running') : t('functions.invoke.submit')" :busy="busy" :error="error" @submit="run">
    <div v-if="allFunctions.length" class="space-y-1">
      <label for="invoke-fn" class="block text-sm font-medium">{{ t('functions.invoke.function') }}</label>
      <select id="invoke-fn" v-model="fn" required class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="" disabled>—</option>
        <option v-for="name in allFunctions" :key="name" :value="name">{{ name }}</option>
      </select>
    </div>
    <TextField v-else v-model="fn" :label="t('functions.invoke.functionText')" autocomplete="off" required />
    <div class="space-y-1">
      <label for="invoke-input" class="block text-sm font-medium">{{ t('functions.invoke.input') }}</label>
      <textarea id="invoke-input" v-model="input" rows="5" spellcheck="false" aria-describedby="invoke-input-help" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-xs dark:border-slate-600 dark:bg-slate-900" />
      <p id="invoke-input-help" class="text-xs text-slate-600 dark:text-slate-400">{{ t('functions.invoke.inputHelp') }}</p>
    </div>
    <TextField v-model="runAs" type="email" :label="t('functions.invoke.as')" :help="t('functions.invoke.asHelp')" autocomplete="off" />
    <TextField v-model="key" :label="t('functions.invoke.key')" :help="t('functions.invoke.keyHelp')" autocomplete="off" />
  </FormDialog>
</template>
