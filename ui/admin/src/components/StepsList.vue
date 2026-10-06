<script setup lang="ts">
import type { Step } from 'backd-js'
import { useI18n } from 'vue-i18n'

// The steps a function reported with ctx.step(): the last is the current one.
defineProps<{ steps: Step[]; omitted?: number }>()
const { t } = useI18n()

const num = (n: number) => new Intl.NumberFormat().format(n)
const progress = (s: Step) => (s.total !== null ? `${num(s.current)} / ${num(s.total)}` : s.current > 0 ? num(s.current) : '')
const percent = (s: Step) => (s.total ? Math.min(100, Math.round((s.current / s.total) * 100)) : null)
const failed = (s: Step) => s.status === 'failed' || s.status === 'timed_out' || s.status === 'cancelled'
</script>

<template>
  <ol class="mt-1 max-w-md space-y-1 text-xs" data-testid="steps">
    <li v-for="(s, i) in steps" :key="s.n" :data-testid="`step-${s.n}`">
      <span class="font-mono">{{ s.n }}. {{ s.name }}</span>
      <span class="ml-1" :class="failed(s) ? 'font-semibold text-red-800 dark:text-red-300' : 'text-slate-600 dark:text-slate-400'">· {{ t(`steps.statuses.${s.status}`) }}</span>
      <span v-if="progress(s)" class="ml-1">· {{ progress(s) }}</span>
      <span v-if="s.duration_ms !== null" class="ml-1 text-slate-600 dark:text-slate-400">· {{ t('history.ms', { n: s.duration_ms }) }}</span>
      <progress v-if="percent(s) !== null" class="mt-0.5 block h-1.5 w-full" :value="percent(s)!" max="100" :aria-label="t('steps.progressOf', { name: s.name })" />
      <span v-if="s.message" class="block text-slate-600 dark:text-slate-400">{{ s.message }}</span>
      <p v-if="omitted && i === 49" class="text-slate-600 dark:text-slate-400">{{ t('steps.omitted', { n: omitted }) }}</p>
    </li>
  </ol>
</template>
