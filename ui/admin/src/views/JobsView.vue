<script setup lang="ts">
import type { JobSummary } from 'backd-js'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FunctionsTabs from '@/components/FunctionsTabs.vue'
import PagerBar from '@/components/PagerBar.vue'
import FormDialog from '@/components/FormDialog.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const route = useRoute()
const session = useSession()
const toasts = useToasts()
const canWrite = computed(() => session.canWrite('functions'))

const PAGE = 50
const filters = reactive({
  function: typeof route.query.function === 'string' ? route.query.function : '',
  status: '' as '' | 'queued' | 'running' | 'done',
  origin: '',
  scheduled: false,
  since: '',
  until: '',
})
const jobs = ref<JobSummary[]>([])
const skip = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')

const instant = (v: string) => (v ? new Date(v).toISOString() : undefined)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await admin.jobs.list({
      function: filters.function.trim() || undefined,
      status: filters.status || undefined,
      origin: filters.origin.trim() || undefined,
      scheduled: filters.scheduled || undefined,
      since: instant(filters.since),
      until: instant(filters.until),
      limit: PAGE,
      skip: skip.value,
    })
    jobs.value = page.items
    hasMore.value = page.has_more
  } catch (e) {
    error.value = errorText(e, t('jobs.loadFailed'))
  } finally {
    loading.value = false
  }
}
function apply() {
  skip.value = 0
  void load()
}
function clear() {
  Object.assign(filters, { function: '', status: '', origin: '', scheduled: false, since: '', until: '' })
  apply()
}
function page(delta: -1 | 1) {
  skip.value = Math.max(0, skip.value + delta * PAGE)
  void load()
}
onMounted(load)

// Cancel and re-run: only function jobs (not backd's own emails and erasures).
const isFunctionJob = (j: JobSummary) => j.email_kind === null && !j.origin.startsWith('backd:')
const canCancel = (j: JobSummary) => isFunctionJob(j) && j.status !== 'done'
const canRerun = (j: JobSummary) => isFunctionJob(j) && j.status === 'done'

const asking = ref<'cancel' | 'rerun' | null>(null)
const target = ref<JobSummary>()
const acting = ref(false)
const dialogOpen = computed({ get: () => asking.value !== null, set: (v) => { if (!v) asking.value = null } })
function ask(kind: 'cancel' | 'rerun', j: JobSummary) {
  target.value = j
  asking.value = kind
}
async function act() {
  const j = target.value!
  const kind = asking.value!
  acting.value = true
  try {
    if (kind === 'cancel') {
      await admin.jobs.cancel(j.id)
      toasts.push(t('jobs.cancel.done'))
    } else {
      const made = await admin.jobs.rerun(j.id)
      toasts.push(t('jobs.rerun.done', { id: made.id }))
    }
    asking.value = null
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
    asking.value = null
    await load()
  } finally {
    acting.value = false
  }
}

const resultText = (j: JobSummary) => (j.result ? `${j.result.status}${j.result.code ? ` · ${j.result.code}` : ''} · ${t('history.ms', { n: j.result.duration_ms })}` : t('jobs.pending'))
</script>

<template>
  <h1 class="text-2xl font-semibold">{{ t('jobs.title') }}</h1>
  <div class="mt-4"><FunctionsTabs /></div>

  <form class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4" role="search" @submit.prevent="apply">
    <TextField v-model="filters.function" :label="t('jobs.function')" autocomplete="off" />
    <div class="space-y-1">
      <label for="job-status" class="block text-sm font-medium">{{ t('jobs.status') }}</label>
      <select id="job-status" v-model="filters.status" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="">{{ t('common.anyStatus') }}</option>
        <option v-for="s in ['queued', 'running', 'done']" :key="s" :value="s">{{ t(`jobs.statuses.${s}`) }}</option>
      </select>
    </div>
    <TextField v-model="filters.origin" :label="t('jobs.origin')" autocomplete="off" />
    <label class="flex items-end gap-2 pb-2 text-sm"><input v-model="filters.scheduled" type="checkbox" /> {{ t('jobs.scheduled') }}</label>
    <TextField v-model="filters.since" type="datetime-local" :label="t('common.from')" />
    <TextField v-model="filters.until" type="datetime-local" :label="t('common.to')" />
    <div class="flex gap-2 sm:col-span-2">
      <AppButton type="submit" :busy="loading">{{ t('common.filter') }}</AppButton>
      <AppButton variant="secondary" @click="clear">{{ t('common.reset') }}</AppButton>
    </div>
  </form>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="jobs-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('jobs.id') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.function') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.status') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.origin') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.attempts') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.created') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.completed') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('jobs.result') }}</th>
          <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('jobs.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="j in jobs" :key="j.id" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 font-mono text-xs break-all">{{ j.id }}</td>
          <td class="px-3 py-2 font-mono text-xs">{{ j.function }}</td>
          <td class="px-3 py-2">{{ t(`jobs.statuses.${j.status}`) }}</td>
          <td class="px-3 py-2 font-mono text-xs">
            {{ j.origin }}<span v-if="j.email_kind"> · {{ t('jobs.email', { kind: j.email_kind }) }}</span>
            <span v-if="j.rerun_of" class="block text-slate-600 dark:text-slate-400">{{ t('jobs.rerunOf', { id: j.rerun_of }) }}</span>
          </td>
          <td class="px-3 py-2" :class="j.attempts > 1 ? 'font-semibold' : ''">{{ j.attempts }}</td>
          <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(j.created_at) }}</td>
          <td class="px-3 py-2 whitespace-nowrap">{{ j.completed_at ? formatDate(j.completed_at) : '—' }}</td>
          <td class="px-3 py-2" :class="j.result && j.result.status !== 'ok' ? 'font-semibold text-red-800 dark:text-red-300' : ''">{{ resultText(j) }}</td>
          <td v-if="canWrite" class="px-3 py-2 text-right whitespace-nowrap">
            <button v-if="canCancel(j)" type="button" class="underline" :aria-label="`${t('jobs.cancel.open')} ${j.id}`" @click="ask('cancel', j)">{{ t('jobs.cancel.open') }}</button>
            <button v-if="canRerun(j)" type="button" class="underline" :aria-label="`${t('jobs.rerun.open')} ${j.id}`" @click="ask('rerun', j)">{{ t('jobs.rerun.open') }}</button>
          </td>
        </tr>
        <tr v-if="!jobs.length && !loading">
          <td :colspan="canWrite ? 9 : 8" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('jobs.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <FormDialog v-model:open="dialogOpen" :title="asking === 'cancel' ? t('jobs.cancel.title') : t('jobs.rerun.title')" :description="asking === 'cancel' ? t('jobs.cancel.body') : t('jobs.rerun.body')" :submit="asking === 'cancel' ? t('jobs.cancel.action') : t('jobs.rerun.action')" :busy="acting" @submit="act">
    <p v-if="target" class="font-mono text-xs break-all">{{ target.function }} · {{ target.id }}</p>
  </FormDialog>

  <PagerBar :skip="skip" :count="jobs.length" :has-more="hasMore" :loading="loading" @page="page" />
</template>
