<script setup lang="ts">
import type { CheckReport } from 'backd-js'
import { PhPlay, PhSealCheck } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import CheckReportView from '@/components/CheckReportView.vue'
import CheckRunning from '@/components/CheckRunning.vue'
import CheckStartDialog from '@/components/CheckStartDialog.vue'
import { useAdmin } from '@/lib/admin'
import { useSchemaChecks } from '@/lib/schemaChecks'

// The Checks tab of a collection: start a schema check of it, follow it, and read the latest report.
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
  <section id="schema-check" class="space-y-4" :aria-label="t('checks.panel')" data-testid="schema-check">
    <AppCard flush>
      <div class="flex flex-wrap items-center gap-3 border-b border-slate-200 px-4 py-2.5 dark:border-slate-800">
        <h2 class="flex items-center gap-2 text-[13px] font-semibold"><PhSealCheck :size="16" class="text-slate-400" aria-hidden="true" />{{ t('checks.panel') }}</h2>
        <RouterLink :to="{ name: 'data-checks', params: { realm } }" class="ml-auto text-xs text-brand-700 hover:underline dark:text-brand-400">{{ t('checks.allChecks') }}</RouterLink>
        <AppButton :disabled="!canStart" data-testid="check-start" @click="asking = true"><PhPlay :size="15" aria-hidden="true" />{{ t('checks.button') }}</AppButton>
      </div>
      <div class="space-y-3 p-4">
        <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('checks.help') }}</p>
        <AppAlert v-if="checks.error.value" kind="error">{{ checks.error.value }}</AppAlert>
        <CheckRunning v-if="checks.running.value" :job="checks.running.value" :percent="checks.percent.value" @cancel="checks.cancel" />
        <p v-if="otherRunning" class="text-sm text-slate-600 dark:text-slate-400">{{ t('checks.otherRunning', { scope: checks.running.value?.function }) }}</p>
        <CheckReportView v-if="report" :realm="realm" :report="report" />
        <p v-else-if="!checks.running.value" class="text-sm text-slate-600 dark:text-slate-400">{{ t('checks.never') }}</p>
      </div>
    </AppCard>
    <CheckStartDialog v-model:open="asking" :scope="`${database}/${collection}`" :estimated="total" :busy="checks.starting.value" @submit="confirm" />
  </section>
</template>
