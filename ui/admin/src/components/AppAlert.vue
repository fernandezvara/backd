<script setup lang="ts">
withDefaults(defineProps<{ kind?: 'info' | 'error' }>(), { kind: 'info' })
</script>

<template>
  <div
    :role="kind === 'error' ? 'alert' : 'status'"
    class="rounded-md border px-3 py-2 text-sm"
    :class="
      kind === 'error'
        ? 'border-red-300 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-950 dark:text-red-100'
        : 'border-slate-300 bg-white text-slate-800 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100'
    "
  >
    <slot />
  </div>
</template>
