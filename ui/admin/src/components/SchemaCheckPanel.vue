<script setup lang="ts">
import type { CheckReport } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import CheckReportView from '@/components/CheckReportView.vue'
import CheckRunning from '@/components/CheckRunning.vue'
import CheckStartDialog from '@/components/CheckStartDialog.vue'
import { useAdmin } from '@/lib/admin'
import { useSchemaChecks } from '@/lib/schemaChecks'

// On a collection's page: start a schema check of it, follow it, and read the latest report.
const props = defineProps<{ realm: string; database: string; collection: string; total?: number }>()
const { t } = useI18n()
const admin = useAdmin()

const report = ref<CheckReport | null>(null)
async function loadReport() {
  try {
    report.value = await admin.dataChecks.get(props.database, props.collection)
  } catch {
    report.value = null // never checked (a 404), or not readable: the page says so
  }
}
const checks = useSchemaChecks(loadReport)
onMounted(async () => {
  await Promise.all([checks.refresh(), loadReport()])
})

const asking = ref(false)
async function confirm() {
  await checks.start({ database: props.database, collection: props.collection })
  asking.value = false
}
const canStart = computed(() => checks.running.value === null)
// Another collection's check is running: say which.
const otherRunning = computed(() => checks.running.value !== null && checks.running.value.function !== `${props.database}/${props.collection}`)
</script>

<template>
  <section class="mt-4 space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" :aria-label="t('checks.panel')" data-testid="schema-check">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="font-semibold">{{ t('checks.panel') }}</h2>
      <div class="flex items-center gap-3 text-sm">
        <RouterLink :to="{ name: 'data-checks', params: { realm } }" class="underline">{{ t('checks.allChecks') }}</RouterLink>
        <AppButton variant="secondary" :disabled="!canStart" data-testid="check-start" @click="asking = true">{{ t('checks.button') }}</AppButton>
      </div>
    </div>
    <AppAlert v-if="checks.error.value" kind="error">{{ checks.error.value }}</AppAlert>
    <CheckRunning v-if="checks.running.value" :job="checks.running.value" :percent="checks.percent.value" @cancel="checks.cancel" />
    <p v-if="otherRunning" class="text-sm text-slate-600 dark:text-slate-400">{{ t('checks.otherRunning', { scope: checks.running.value?.function }) }}</p>
    <CheckReportView v-if="report" :realm="realm" :report="report" />
    <p v-else-if="!checks.running.value" class="text-sm text-slate-600 dark:text-slate-400">{{ t('checks.never') }}</p>
    <CheckStartDialog v-model:open="asking" :scope="`${database}/${collection}`" :estimated="total" :busy="checks.starting.value" @submit="confirm" />
  </section>
</template>
