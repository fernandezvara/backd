<script setup lang="ts">
// The surface of the interface: a bordered white (dark: slate) box. Tables sit in one with `flush`.
defineProps<{ flush?: boolean }>()
</script>

<template>
  <div class="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900" :class="flush ? 'overflow-x-auto' : 'p-4'">
    <slot />
  </div>
</template>
