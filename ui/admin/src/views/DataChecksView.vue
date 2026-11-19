<script setup lang="ts">
import AppCard from '@/components/AppCard.vue'
import type { CheckReport } from 'backd-js'
import { PhArrowLeft, PhArrowsLeftRight, PhPlay, PhWarning } from '@phosphor-icons/vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import MigrationsPanel from '@/components/MigrationsPanel.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppBadge from '@/components/AppBadge.vue'
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
  <RouterLink :to="{ name: 'data', params: { realm } }" class="inline-flex items-center gap-1 text-sm text-slate-600 hover:underline dark:text-slate-400"><PhArrowLeft :size="14" aria-hidden="true" />{{ t('data.title') }}</RouterLink>
  <PageHeader :title="t('checks.title')" :description="t('checks.help')" class="mt-2">
    <AppButton :disabled="!canStart" data-testid="check-all" @click="ask()"><PhPlay :size="15" aria-hidden="true" />{{ t('checks.checkAll') }}</AppButton>
  </PageHeader>
  <p class="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"><PhWarning :size="18" class="mt-px shrink-0" aria-hidden="true" />{{ t('checks.start.warning') }}</p>

  <AppAlert v-if="checks.error.value" kind="error" class="mt-4">{{ checks.error.value }}</AppAlert>
  <div v-if="checks.running.value" class="mt-4"><CheckRunning :job="checks.running.value" :percent="checks.percent.value" @cancel="checks.cancel" /></div>

  <AppCard flush class="mt-4">
    <table class="w-full text-left text-sm" data-testid="checks-table">
      <thead>
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('checks.collection') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.scanned') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.invalid') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('checks.lastCheck') }}</th>
          <th scope="col" class="px-3 py-2"><span class="sr-only">{{ t('checks.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="it in checks.listing.value?.items ?? []" :key="`${it.database}/${it.collection}`">
          <tr class="border-b border-slate-100 align-top dark:border-slate-800" :data-testid="`check-${it.database}-${it.collection}`">
            <td class="px-3 py-2 font-mono text-xs">
              <RouterLink :to="{ name: 'data-collection', params: { realm, database: it.database, collection: it.collection } }" class="text-brand-700 hover:underline dark:text-brand-400">{{ it.database }}/{{ it.collection }}</RouterLink>
            </td>
            <template v-if="it.report">
              <td class="px-3 py-2">{{ it.report.scanned.toLocaleString() }}</td>
              <td class="px-3 py-2"><AppBadge :kind="it.report.invalid ? 'warn' : 'ok'">{{ it.report.invalid.toLocaleString() }}</AppBadge><span v-if="!it.report.complete" class="text-slate-600 dark:text-slate-400"> · {{ t('checks.incomplete') }}</span></td>
              <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(it.report.finished_at) }}</td>
            </template>
            <td v-else colspan="3" class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ t('checks.never') }}</td>
            <td class="px-3 py-2 text-right whitespace-nowrap">
              <AppButton variant="secondary" class="!h-7 !px-2 !text-xs" :disabled="!canStart" :aria-label="`${t('checks.checkOne')} ${it.database}/${it.collection}`" @click="ask(it.database, it.collection)"><PhPlay :size="12" aria-hidden="true" />{{ t('checks.checkOne') }}</AppButton>
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
  </AppCard>

  <section class="mt-8" aria-labelledby="migrations-h">
    <h2 id="migrations-h" class="mb-1 flex items-center gap-2 text-[13px] font-semibold"><PhArrowsLeftRight :size="15" class="text-slate-400" aria-hidden="true" />{{ t('checksTabs.migrations') }}</h2>
    <MigrationsPanel />
  </section>

  <CheckStartDialog v-model:open="asking" :scope="scopeText" :busy="checks.starting.value" @submit="confirm" />
</template>
