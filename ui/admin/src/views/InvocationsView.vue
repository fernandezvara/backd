<script setup lang="ts">
import type { InvocationRecord } from 'backd-js'
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FunctionsTabs from '@/components/FunctionsTabs.vue'
import StepsList from '@/components/StepsList.vue'
import PagerBar from '@/components/PagerBar.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const route = useRoute()

const PAGE = 50
const filters = reactive({ function: typeof route.query.function === 'string' ? route.query.function : '', requestId: '', since: '', until: '' })
const records = ref<InvocationRecord[]>([])
const skip = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')

const instant = (v: string) => (v ? new Date(v).toISOString() : undefined)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await admin.invocations.list({
      function: filters.function.trim() || undefined,
      requestId: filters.requestId.trim() || undefined,
      since: instant(filters.since),
      until: instant(filters.until),
      limit: PAGE,
      skip: skip.value,
    })
    records.value = page.items
    hasMore.value = page.has_more
  } catch (e) {
    error.value = errorText(e, t('history.loadFailed'))
  } finally {
    loading.value = false
  }
}
function apply() {
  skip.value = 0
  void load()
}
function clear() {
  Object.assign(filters, { function: '', requestId: '', since: '', until: '' })
  apply()
}
function page(delta: -1 | 1) {
  skip.value = Math.max(0, skip.value + delta * PAGE)
  void load()
}
onMounted(load)

const statusClass = (s: string) => (s === 'ok' ? '' : 'font-semibold text-red-800 dark:text-red-300')
</script>

<template>
  <h1 class="text-2xl font-semibold">{{ t('history.title') }}</h1>
  <div class="mt-4"><FunctionsTabs /></div>
  <AppAlert class="mb-4">{{ t('history.warning') }}</AppAlert>

  <form class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4" role="search" @submit.prevent="apply">
    <TextField v-model="filters.function" :label="t('history.function')" autocomplete="off" />
    <TextField v-model="filters.requestId" :label="t('history.requestId')" autocomplete="off" />
    <TextField v-model="filters.since" type="datetime-local" :label="t('common.from')" />
    <TextField v-model="filters.until" type="datetime-local" :label="t('common.to')" />
    <div class="flex gap-2 sm:col-span-2 lg:col-span-4">
      <AppButton type="submit" :busy="loading">{{ t('common.filter') }}</AppButton>
      <AppButton variant="secondary" @click="clear">{{ t('common.reset') }}</AppButton>
    </div>
  </form>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="history-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('history.time') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.function') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.mode') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.status') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.duration') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.origin') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.actor') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('history.logs') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in records" :key="r.id" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(r.at) }}</td>
          <td class="px-3 py-2 font-mono text-xs">{{ r.function }}</td>
          <td class="px-3 py-2">{{ r.mode }}</td>
          <td class="px-3 py-2" :class="statusClass(r.status)">{{ r.status }}<span v-if="r.code" class="font-mono text-xs"> · {{ r.code }}</span></td>
          <td class="px-3 py-2 whitespace-nowrap">{{ t('history.ms', { n: r.duration_ms }) }}</td>
          <td class="px-3 py-2 font-mono text-xs">{{ r.origin ?? '—' }}</td>
          <td class="px-3 py-2 font-mono text-xs break-all">{{ r.actor }}</td>
          <td class="px-3 py-2">
            <details v-if="r.logs.length || r.steps.length || r.job_id || r.parent_id || r.request_id">
              <summary class="cursor-pointer text-xs underline">{{ t('common.details') }}</summary>
              <dl class="mt-1 space-y-1 text-xs">
                <div v-if="r.request_id"><dt class="inline text-slate-600 dark:text-slate-400">{{ t('history.requestId') }}: </dt><dd class="inline font-mono">{{ r.request_id }}</dd></div>
                <div v-if="r.job_id">
                  <dt class="inline text-slate-600 dark:text-slate-400">{{ t('history.job') }}: </dt>
                  <dd class="inline font-mono">{{ r.job_id }}</dd>
                </div>
                <div v-if="r.parent_id"><dt class="inline text-slate-600 dark:text-slate-400">{{ t('history.parent') }}: </dt><dd class="inline font-mono">{{ r.parent_id }}</dd></div>
              </dl>
              <StepsList v-if="r.steps.length" :steps="r.steps" :omitted="r.steps_omitted" />
              <pre v-if="r.logs.length" class="mt-1 max-h-64 max-w-md overflow-auto rounded bg-slate-100 p-2 text-xs dark:bg-slate-800" tabindex="0" data-testid="log-lines">{{ r.logs.map((l) => `[${l.level}] ${l.line}`).join('\n') }}</pre>
              <p v-else class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('history.noLogs') }}</p>
            </details>
            <span v-else class="text-xs text-slate-600 dark:text-slate-400">{{ t('history.noLogs') }}</span>
          </td>
        </tr>
        <tr v-if="!records.length && !loading">
          <td colspan="8" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('history.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <PagerBar :skip="skip" :count="records.length" :has-more="hasMore" :loading="loading" @page="page" />
</template>
