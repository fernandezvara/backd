<script setup lang="ts">
// A small status label. The text is always there; the color only helps.
withDefaults(defineProps<{ kind?: 'neutral' | 'ok' | 'warn' | 'error' | 'violet' | 'blue' }>(), { kind: 'neutral' })
</script>

<template>
  <span
    class="inline-block rounded-full px-2 py-0.5 text-xs font-medium"
    :class="{
      'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300': kind === 'neutral',
      'bg-brand-50 text-brand-800 dark:bg-brand-800 dark:text-brand-50': kind === 'ok',
      'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100': kind === 'warn',
      'bg-red-100 text-red-900 dark:bg-red-900/40 dark:text-red-100': kind === 'error',
      'bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300': kind === 'violet',
      'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300': kind === 'blue',
    }"
  >
    <slot />
  </span>
</template>
