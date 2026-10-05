<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useToasts } from '@/stores/toasts'

const { t } = useI18n()
const toasts = useToasts()
</script>

<template>
  <div class="fixed right-4 bottom-4 z-50 flex w-[min(22rem,92vw)] flex-col gap-2" aria-live="polite">
    <div
      v-for="toast in toasts.items"
      :key="toast.id"
      :role="toast.kind === 'error' ? 'alert' : 'status'"
      class="flex items-start gap-3 rounded-md border px-3 py-2 text-sm shadow"
      :class="toast.kind === 'error' ? 'border-red-300 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-950 dark:text-red-100' : 'border-slate-300 bg-white text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100'"
    >
      <span class="flex-1">{{ toast.text }}</span>
      <button type="button" class="underline" @click="toasts.dismiss(toast.id)">{{ t('common.dismiss') }}</button>
    </div>
  </div>
</template>
