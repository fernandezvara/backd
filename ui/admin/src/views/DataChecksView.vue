<script setup lang="ts">
import type { CheckReport } from 'backd-js'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import CheckReportView from '@/components/CheckReportView.vue'
import CheckRunning from '@/components/CheckRunning.vue'
import CheckStartDialog from '@/components/CheckStartDialog.vue'
import { useAdmin } from '@/lib/admin'
import { formatDate } from '@/lib/format'
import { useSchemaChecks } from '@/lib/schemaChecks'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()

// Reports opened in full, by "<database>/<collection>"; reloaded when a check finishes.
const open = reactive<Record<string, CheckReport | 'loading' | 'error'>>({})
async function loadReport(key: string) {
  const [database, collection] = [key.slice(0, key.indexOf('/')), key.slice(key.indexOf('/') + 1)]
  try {
    open[key] = await admin.dataChecks.get(database, collection)
  } catch {
    open[key] = 'error'
  }
}
const checks = useSchemaChecks(() => {
  for (const key of Object.keys(open)) void loadReport(key)
})
onMounted(checks.refresh)

function toggle(key: string, event: Event) {
  if ((event.target as HTMLDetailsElement).open) {
    open[key] = 'loading'
    void loadReport(key)
  } else delete open[key]
}

// The confirmation: the whole realm, or one collection.
const asking = ref(false)
const scope = ref<{ database?: string; collection?: string }>({})
function ask(database?: string, collection?: string) {
  scope.value = { database, collection }
  asking.value = true
}
async function confirm() {
  await checks.start(scope.value)
  asking.value = false
}
const scopeText = computed(() => (scope.value.collection ? `${scope.value.database}/${scope.value.collection}` : scope.value.database ? scope.value.database : t('checks.start.everything')))
const canStart = computed(() => checks.running.value === null)
</script>

<template>
  <nav class="text-sm" aria-label="Breadcrumb"><RouterLink :to="{ name: 'data', params: { realm } }" class="underline">{{ t('nav.data') }}</RouterLink></nav>
  <div class="mt-2 flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('checks.title') }}</h1>
    <AppButton :disabled="!canStart" data-testid="check-all" @click="ask()">{{ t('checks.checkAll') }}</AppButton>
  </div>
  <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('checks.help') }}</p>
  <p class="mt-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100">{{ t('checks.start.warning') }}</p>

  <AppAlert v-if="checks.error.value" kind="error" class="mt-4">{{ checks.error.value }}</AppAlert>
  <div v-if="checks.running.value" class="mt-4"><CheckRunning :job="checks.running.value" :percent="checks.percent.value" @cancel="checks.cancel" /></div>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="checks-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('checks.collection') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.scanned') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.invalid') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.finished') }}</th>
          <th scope="col" class="px-3 py-2"><span class="sr-only">{{ t('checks.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="it in checks.listing.value?.items ?? []" :key="`${it.database}/${it.collection}`">
          <tr class="border-b border-slate-100 align-top dark:border-slate-800" :data-testid="`check-${it.database}-${it.collection}`">
            <td class="px-3 py-2 font-mono text-xs">
              <RouterLink :to="{ name: 'data-collection', params: { realm, database: it.database, collection: it.collection } }" class="underline">{{ it.database }}/{{ it.collection }}</RouterLink>
            </td>
            <template v-if="it.report">
              <td class="px-3 py-2">{{ it.report.scanned }}</td>
              <td class="px-3 py-2" :class="it.report.invalid ? 'font-semibold text-red-800 dark:text-red-300' : ''">{{ it.report.invalid }}<span v-if="!it.report.complete" class="font-normal text-slate-600 dark:text-slate-400"> · {{ t('checks.incomplete') }}</span></td>
              <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(it.report.finished_at) }}</td>
            </template>
            <td v-else colspan="3" class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ t('checks.never') }}</td>
            <td class="px-3 py-2 text-right whitespace-nowrap">
              <button type="button" class="underline disabled:opacity-60" :disabled="!canStart" :aria-label="`${t('checks.checkOne')} ${it.database}/${it.collection}`" @click="ask(it.database, it.collection)">{{ t('checks.checkOne') }}</button>
            </td>
          </tr>
          <tr v-if="it.report" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
            <td colspan="5" class="px-3 pb-2">
              <details :data-testid="`report-${it.database}-${it.collection}`" @toggle="toggle(`${it.database}/${it.collection}`, $event)">
                <summary class="cursor-pointer text-xs underline">{{ t('checks.showReport') }}</summary>
                <p v-if="open[`${it.database}/${it.collection}`] === 'loading'" class="mt-1 text-xs" role="status">{{ t('common.loading') }}</p>
                <AppAlert v-else-if="open[`${it.database}/${it.collection}`] === 'error'" kind="error" class="mt-1">{{ t('checks.loadFailed') }}</AppAlert>
                <CheckReportView v-else-if="open[`${it.database}/${it.collection}`]" :realm="realm" :report="open[`${it.database}/${it.collection}`] as CheckReport" class="mt-2" />
              </details>
            </td>
          </tr>
        </template>
        <tr v-if="!checks.listing.value?.items.length">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('checks.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <CheckStartDialog v-model:open="asking" :scope="scopeText" :busy="checks.starting.value" @submit="confirm" />
</template>
