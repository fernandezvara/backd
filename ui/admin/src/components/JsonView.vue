<script setup lang="ts">
import { computed } from 'vue'

// JSON as plain text in a scrollable, focusable block: large schemas stay
// readable, and nothing in the value can ever become markup.
const props = defineProps<{ value: unknown; label: string }>()
const text = computed(() => JSON.stringify(props.value, null, 2) ?? '')
</script>

<template>
  <pre class="max-h-96 overflow-auto rounded bg-slate-100 p-3 font-mono text-xs dark:bg-slate-800" tabindex="0" role="region" :aria-label="label">{{ text }}</pre>
</template>
