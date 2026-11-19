<script setup lang="ts" generic="T extends string">
import type { Component } from 'vue'

// A short list of exclusive choices side by side, the active one filled. Arrow keys move between them.
defineProps<{ label: string; options: { value: T; label: string; title?: string; icon?: Component; testid?: string }[]; iconOnly?: boolean }>()
const model = defineModel<T>({ required: true })
function move(e: KeyboardEvent, by: 1 | -1) {
  const el = e.currentTarget as HTMLElement
  const to = by === 1 ? el.nextElementSibling : el.previousElementSibling
  if (to instanceof HTMLElement) {
    to.click()
    to.focus()
  }
}
</script>

<template>
  <div role="radiogroup" :aria-label="label" class="inline-flex overflow-hidden rounded-md border border-slate-300 text-sm dark:border-slate-700">
    <button
      v-for="o in options"
      :key="o.value"
      type="button"
      role="radio"
      class="flex h-8 items-center gap-1.5 border-l border-slate-300 px-3 first:border-l-0 dark:border-slate-700"
      :class="model === o.value ? 'bg-brand-700 text-white' : 'bg-white text-slate-700 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800'"
      :aria-checked="model === o.value"
      :aria-label="iconOnly ? (o.title ?? o.label) : undefined"
      :title="o.title ?? o.label"
      :tabindex="model === o.value ? 0 : -1"
      :data-testid="o.testid"
      @click="model = o.value"
      @keydown.right.prevent="move($event, 1)"
      @keydown.left.prevent="move($event, -1)"
    >
      <component :is="o.icon" v-if="o.icon" :size="15" aria-hidden="true" />
      <span :class="iconOnly && o.icon ? 'sr-only' : ''">{{ o.label }}</span>
    </button>
  </div>
</template>
