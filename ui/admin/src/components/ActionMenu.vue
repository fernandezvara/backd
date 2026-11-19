<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { PhDotsThree } from '@phosphor-icons/vue'

// A "more" button that opens a short list of actions; it closes on a choice, Escape or a click outside.
export interface MenuItem {
  id: string
  label: string
  danger?: boolean
  hidden?: boolean
  testid?: string
}
defineProps<{ label: string; items: MenuItem[] }>()
const emit = defineEmits<{ select: [id: string] }>()
const open = ref(false)
const root = ref<HTMLElement>()

const outside = (e: MouseEvent) => {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}
const esc = (e: KeyboardEvent) => {
  if (e.key === 'Escape') open.value = false
}
onMounted(() => {
  document.addEventListener('click', outside)
  document.addEventListener('keydown', esc)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', outside)
  document.removeEventListener('keydown', esc)
})
function pick(id: string) {
  open.value = false
  emit('select', id)
}
</script>

<template>
  <div ref="root" class="relative">
    <button type="button" class="inline-flex size-8 items-center justify-center rounded-md border border-slate-300 bg-white hover:bg-slate-100 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800" :aria-label="label" aria-haspopup="menu" :aria-expanded="open" data-testid="action-menu" @click="open = !open">
      <PhDotsThree :size="18" weight="bold" aria-hidden="true" />
    </button>
    <div v-if="open" role="menu" class="absolute right-0 top-full z-30 mt-1 w-56 rounded-lg border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
      <template v-for="item in items" :key="item.id">
        <button v-if="!item.hidden" type="button" role="menuitem" class="flex h-8 w-full items-center px-3 text-left text-[13px] hover:bg-slate-100 dark:hover:bg-slate-800" :class="item.danger ? 'text-red-700 dark:text-red-400' : ''" :data-testid="item.testid" @click="pick(item.id)">{{ item.label }}</button>
      </template>
    </div>
  </div>
</template>
