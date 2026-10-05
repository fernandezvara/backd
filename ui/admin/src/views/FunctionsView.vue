<script setup lang="ts">
import { Job } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FormDialog from '@/components/FormDialog.vue'
import FunctionsTabs from '@/components/FunctionsTabs.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
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
  overlap: string
  calls: string[]
  secrets: string[]
  network: string[]
  invoke?: string
  rate_limit?: { per: string; limit: number; window: string }
  retry?: { attempts: number; backoff: string; max_backoff: string }
}

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const realmConfig = useRealmConfig()

const canConfig = computed(() => session.canRead('config'))
const canInvoke = computed(() => session.canWrite('functions'))
const loadError = ref('')
onMounted(async () => {
  await realmConfig.load()
  if (canConfig.value && !realmConfig.config) loadError.value = t('functions.loadFailed')
})

const databases = computed(() => {
  const dbs = (realmConfig.config?.databases ?? {}) as Record<string, { functions?: FunctionConfig[] }>
  return Object.entries(dbs)
    .map(([name, db]) => ({ name, functions: db.functions ?? [] }))
    .filter((db) => db.functions.length)
    .sort((a, b) => a.name.localeCompare(b.name))
})
const allFunctions = computed(() => databases.value.flatMap((db) => db.functions.map((f) => `${db.name}/${f.name}`)))
const list = (xs: string[]) => (xs.length ? xs.join(', ') : t('functions.noneValue'))
const bytes = (n: number) => (n >= 1 << 20 ? `${n / (1 << 20)} MiB` : n >= 1 << 10 ? `${n / (1 << 10)} KiB` : `${n} B`)

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
  <div class="flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('functions.title') }}</h1>
    <AppButton v-if="canInvoke" @click="show()">{{ t('functions.invoke.open') }}</AppButton>
  </div>
  <div class="mt-4"><FunctionsTabs /></div>

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

  <section v-for="db in databases" :key="db.name" class="mb-8" :aria-labelledby="`db-${db.name}`">
    <h2 :id="`db-${db.name}`" class="text-lg font-semibold">{{ t('functions.database', { name: db.name }) }}</h2>
    <div class="mt-3 grid gap-4 lg:grid-cols-2">
      <article v-for="f in db.functions" :key="f.name" class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" :data-testid="`fn-${db.name}-${f.name}`">
        <div class="flex flex-wrap items-center gap-2">
          <h3 class="font-mono text-sm font-semibold">{{ f.name }}</h3>
          <span class="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-700">{{ t(`functions.modes.${f.mode}`) }}</span>
          <span v-if="f.internal" class="rounded bg-amber-100 px-2 py-0.5 text-xs text-amber-900 dark:bg-amber-900 dark:text-amber-100">{{ t('functions.internal') }}</span>
          <span v-if="f.admin" class="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-700">{{ t('functions.admin') }}</span>
          <span v-if="f.email" class="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-700">{{ t('functions.email') }}</span>
          <span v-if="f.dev_only" class="rounded bg-slate-200 px-2 py-0.5 text-xs dark:bg-slate-700">{{ t('functions.devOnly') }}</span>
          <AppButton v-if="canInvoke" variant="secondary" class="ml-auto" @click="show(`${db.name}/${f.name}`)">{{ t('functions.invoke.submit') }}</AppButton>
        </div>
        <dl class="mt-3 grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 text-sm">
          <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.schedule') }}</dt>
          <dd class="font-mono text-xs">{{ f.schedule || t('functions.noSchedule') }}<span v-if="f.schedule && f.overlap === 'skip'" class="font-sans text-slate-600 dark:text-slate-400"> · {{ t('functions.overlapSkip') }}</span></dd>
          <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.limits') }}</dt>
          <dd>{{ t('functions.timeout') }} {{ prettyDuration(f.timeout) }} · {{ t('functions.memory') }} {{ bytes(f.memory) }} · {{ t('functions.maxOutput') }} {{ bytes(f.max_output) }} · {{ t('functions.concurrency') }} {{ f.concurrency }}</dd>
          <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.calls') }}</dt>
          <dd class="font-mono text-xs">{{ list(f.calls) }}</dd>
          <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.secrets') }}</dt>
          <dd class="font-mono text-xs">{{ list(f.secrets) }}</dd>
          <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.network') }}</dt>
          <dd class="font-mono text-xs">{{ list(f.network) }}</dd>
          <template v-if="f.retry">
            <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.retry') }}</dt>
            <dd>{{ t('functions.retryText', { attempts: f.retry.attempts, backoff: prettyDuration(f.retry.backoff) }) }}</dd>
          </template>
          <template v-if="f.rate_limit">
            <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.rateLimit') }}</dt>
            <dd>{{ t('functions.rateText', { limit: f.rate_limit.limit, per: f.rate_limit.per, window: prettyDuration(f.rate_limit.window) }) }}</dd>
          </template>
          <template v-if="f.invoke">
            <dt class="text-slate-600 dark:text-slate-400">{{ t('functions.invokeRule') }}</dt>
            <dd class="font-mono text-xs">{{ f.invoke }}</dd>
          </template>
        </dl>
        <p class="mt-3 text-xs text-slate-600 dark:text-slate-400">{{ t('functions.file', { file: f.file }) }}</p>
      </article>
    </div>
  </section>

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
