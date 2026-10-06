<script setup lang="ts">
import type { JobSummary } from 'backd-js'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'

// The schema check running now: where it is, with a way to cancel it.
defineProps<{ job: JobSummary; percent: number }>()
defineEmits<{ cancel: [] }>()
const { t } = useI18n()
</script>

<template>
  <div class="rounded-lg border border-brand-700 bg-white p-4 dark:bg-slate-900" role="status" data-testid="check-running">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="font-medium">{{ job.status === 'queued' ? t('checks.queued') : t('checks.running', { scope: job.function }) }}</p>
      <AppButton variant="secondary" @click="$emit('cancel')">{{ t('checks.cancel') }}</AppButton>
    </div>
    <p v-if="job.progress" class="mt-2 text-sm"><span class="font-mono">{{ job.progress.name }}</span> · {{ percent }}% <span v-if="job.progress.message" class="text-slate-600 dark:text-slate-400">· {{ job.progress.message }}</span></p>
    <progress class="mt-2 block h-2 w-full" :value="percent" max="100" :aria-label="t('checks.progress')" />
    <p v-if="job.status === 'queued'" class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t('checks.needsWorker') }}</p>
  </div>
</template>
