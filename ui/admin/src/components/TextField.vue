<script setup lang="ts">
import { useId } from 'vue'

defineOptions({ inheritAttrs: false })
defineProps<{ label: string; type?: string; autocomplete?: string; error?: string; help?: string; required?: boolean }>()
const model = defineModel<string>({ default: '' })
const id = useId()
</script>

<template>
  <div class="space-y-1">
    <label :for="id" class="block text-sm font-medium">{{ label }}</label>
    <input
      :id="id"
      v-model="model"
      :type="type ?? 'text'"
      :autocomplete="autocomplete"
      :required="required"
      v-bind="$attrs"
      :aria-invalid="error ? true : undefined"
      :aria-describedby="error ? `${id}-error` : help ? `${id}-help` : undefined"
      class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900"
    />
    <p v-if="help && !error" :id="`${id}-help`" class="text-xs text-slate-600 dark:text-slate-400">{{ help }}</p>
    <p v-if="error" :id="`${id}-error`" class="text-xs text-red-700 dark:text-red-400">{{ error }}</p>
  </div>
</template>
