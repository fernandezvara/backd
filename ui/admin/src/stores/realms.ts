import { defineStore } from 'pinia'
import { ref } from 'vue'
import { validRealm } from '@/lib/realm'

const KEY = 'backd-admin:realms'

function read(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((r): r is string => typeof r === 'string' && validRealm(r)) : []
  } catch {
    return []
  }
}

/**
 * The realms this browser has signed in to, for the realm switcher. Kept in this browser only
 * (no server involved): a realm is added when a session opens in it.
 */
export const useRealms = defineStore('realms', () => {
  const list = ref<string[]>(read())
  function remember(realm: string) {
    if (!realm || list.value.includes(realm)) return
    list.value = [...list.value, realm]
    try {
      localStorage.setItem(KEY, JSON.stringify(list.value))
    } catch {
      // not persisted; the list lasts for this page
    }
  }
  function forget(realm: string) {
    list.value = list.value.filter((r) => r !== realm)
    try {
      localStorage.setItem(KEY, JSON.stringify(list.value))
    } catch {
      // not persisted
    }
  }
  return { list, remember, forget }
})
