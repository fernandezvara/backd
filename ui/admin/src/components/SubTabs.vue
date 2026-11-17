<script setup lang="ts">
import { useId } from 'vue'

// Tabs inside one page (no route change): a row of buttons that choose which panel shows. The page
// renders the panels itself with `v-if` on the model.
defineProps<{ label: string; tabs: { id: string; label: string }[] }>()
const model = defineModel<string>({ required: true })
const id = useId()
</script>

<template>
  <div role="tablist" :aria-label="label" class="mb-4 flex flex-wrap gap-1 border-b border-slate-200 dark:border-slate-800">
    <button
      v-for="tab in tabs"
      :id="`${id}-${tab.id}`"
      :key="tab.id"
      type="button"
      role="tab"
      class="-mb-px border-b-2 px-3 py-2 text-sm"
      :class="model === tab.id ? 'border-brand-700 font-semibold text-brand-800 dark:border-brand-400 dark:text-brand-100' : 'border-transparent text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'"
      :aria-selected="model === tab.id"
      :data-testid="`tab-${tab.id}`"
      @click="model = tab.id"
    >
      {{ tab.label }}
    </button>
  </div>
</template>
