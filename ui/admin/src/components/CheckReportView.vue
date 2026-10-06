<script setup lang="ts">
import type { CheckReport } from 'backd-js'
import { useI18n } from 'vue-i18n'
import { formatDate } from '@/lib/format'

// A collection's schema check report: what it read, and each document that failed.
defineProps<{ realm: string; report: CheckReport }>()
const { t } = useI18n()
</script>

<template>
  <div class="space-y-2 text-sm" data-testid="check-report">
    <p>
      {{ t('checks.report.summary', { scanned: report.scanned, invalid: report.invalid }) }}
      <span v-if="!report.complete" class="font-semibold text-amber-800 dark:text-amber-300"> · {{ t(`checks.report.stopped.${report.stopped_by ?? 'limit'}`) }}</span>
    </p>
    <p class="text-xs text-slate-600 dark:text-slate-400">{{ t('checks.report.meta', { at: formatDate(report.finished_at), schema: report.schema_hash }) }}</p>
    <p v-if="!report.invalid" class="font-medium text-green-800 dark:text-green-300">{{ t('checks.report.clean') }}</p>
    <div v-else class="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
      <table class="w-full text-left text-sm">
        <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
          <tr>
            <th scope="col" class="px-3 py-2">{{ t('checks.report.document') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('checks.report.problems') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in report.documents" :key="d.id" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800" :data-testid="`invalid-${d.id}`">
            <td class="px-3 py-2">
              <RouterLink :to="{ name: 'data-doc', params: { realm, database: report.database, collection: report.collection, id: d.id } }" class="font-mono text-xs underline">{{ d.id }}</RouterLink>
              <span v-if="d.deleted" class="ml-2 rounded bg-slate-200 px-1.5 py-0.5 text-xs dark:bg-slate-700">{{ t('checks.report.trash') }}</span>
            </td>
            <td class="px-3 py-2">
              <ul class="space-y-0.5">
                <li v-for="(p, i) in d.problems" :key="i"><span class="font-mono text-xs">{{ p.path || t('checks.report.root') }}</span>: {{ p.reason }}</li>
                <li v-if="d.more_problems" class="text-slate-600 dark:text-slate-400">{{ t('checks.report.more', { n: d.more_problems }) }}</li>
              </ul>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
