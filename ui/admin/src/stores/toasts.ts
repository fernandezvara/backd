import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast {
  id: number
  kind: 'info' | 'error'
  text: string
}

/** Short messages that confirm what just happened; they go away by themselves. */
export const useToasts = defineStore('toasts', () => {
  const items = ref<Toast[]>([])
  let next = 1
  function push(text: string, kind: Toast['kind'] = 'info', ms = 6000) {
    const id = next++
    items.value.push({ id, kind, text })
    if (ms > 0) setTimeout(() => dismiss(id), ms)
  }
  function dismiss(id: number) {
    items.value = items.value.filter((t) => t.id !== id)
  }
  return { items, push, dismiss }
})
