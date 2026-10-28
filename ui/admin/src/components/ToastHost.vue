<script setup lang="ts">
import { PhCheckCircle, PhWarningCircle } from '@phosphor-icons/vue'
import { useI18n } from 'vue-i18n'
import { useToasts } from '@/stores/toasts'

const { t } = useI18n()
const toasts = useToasts()
</script>

<template>
  <!-- Under the bar, where the eye is after pressing a button at the top of a page. -->
  <div class="fixed top-16 right-4 z-50 flex w-[min(24rem,92vw)] flex-col gap-2" aria-live="polite">
    <div
      v-for="toast in toasts.items"
      :key="toast.id"
      :role="toast.kind === 'error' ? 'alert' : 'status'"
      class="flex items-start gap-3 rounded-md border px-3 py-2.5 text-sm shadow-lg"
      :class="toast.kind === 'error' ? 'border-red-300 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-950 dark:text-red-100' : 'border-brand-700 bg-white text-slate-900 dark:border-brand-400 dark:bg-slate-900 dark:text-slate-100'"
      data-testid="toast"
    >
      <PhWarningCircle v-if="toast.kind === 'error'" :size="18" class="mt-px shrink-0" aria-hidden="true" />
      <PhCheckCircle v-else :size="18" class="mt-px shrink-0 text-brand-700 dark:text-brand-400" aria-hidden="true" />
      <span class="flex-1">{{ toast.text }}</span>
      <button type="button" class="underline" @click="toasts.dismiss(toast.id)">{{ t('common.dismiss') }}</button>
    </div>
  </div>
</template>
