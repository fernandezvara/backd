<script setup lang="ts">
// A file tree and the file it shows, joined in one frame: the tree takes the height of the code,
// and each side scrolls on its own.
</script>

<template>
  <div class="card-soft flex h-[min(42rem,calc(100vh-18rem))] min-h-[22rem] overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <aside class="flex w-72 shrink-0 flex-col border-r border-slate-200 dark:border-slate-800">
      <div v-if="$slots.toolbar" class="shrink-0 border-b border-slate-200 p-2 dark:border-slate-800"><slot name="toolbar" /></div>
      <div class="min-h-0 flex-1 overflow-auto"><slot name="tree" /></div>
      <div v-if="$slots.foot" class="shrink-0 border-t border-slate-200 p-2 text-xs text-slate-500 dark:border-slate-800"><slot name="foot" /></div>
    </aside>
    <div class="min-h-0 min-w-0 flex-1"><slot /></div>
  </div>
</template>
