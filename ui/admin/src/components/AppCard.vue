<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

// The surface of the interface: a bordered white (dark: slate) box. Tables sit in one with `flush`;
// when the table is wider than the screen the box scrolls, and then the keyboard can reach it.
defineProps<{ flush?: boolean }>()
const box = ref<HTMLElement>()
const scrolls = ref(false)
let watcher: ResizeObserver | undefined

const measure = () => {
  scrolls.value = !!box.value && box.value.scrollWidth > box.value.clientWidth
}
onMounted(() => {
  measure()
  if (typeof ResizeObserver === 'undefined' || !box.value) return
  watcher = new ResizeObserver(measure)
  watcher.observe(box.value)
  if (box.value.firstElementChild) watcher.observe(box.value.firstElementChild)
})
onBeforeUnmount(() => watcher?.disconnect())
</script>

<template>
  <div ref="box" class="relative rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900" :class="flush ? 'overflow-x-auto' : 'p-4'" :tabindex="flush && scrolls ? 0 : undefined">
    <slot />
  </div>
</template>
