<script setup lang="ts">
import { computed } from 'vue'

// A round badge with the initials of an email (or a name): there is no picture to show.
const props = defineProps<{ name: string; size?: 'sm' | 'lg'; muted?: boolean }>()
const initials = computed(() => {
  const base = props.name.split('@')[0] ?? ''
  const parts = base.split(/[\s._+-]+/).filter(Boolean)
  const letters = parts.length > 1 ? parts[0]![0]! + parts[1]![0]! : base.slice(0, 2)
  return letters.toUpperCase() || '?'
})
</script>

<template>
  <span
    class="grid shrink-0 place-items-center rounded-full font-semibold"
    :class="[size === 'lg' ? 'size-14 text-xl' : 'size-7 text-xs', muted ? 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300' : size === 'lg' ? 'bg-brand-700 text-white' : 'bg-slate-200 text-slate-800 dark:bg-slate-700 dark:text-slate-100']"
    aria-hidden="true"
  >{{ initials }}</span>
</template>
