import { defineStore } from 'pinia'
import { reactive } from 'vue'

/**
 * The names a page learns after it loads (a user's email, an organization's name), for the
 * breadcrumb to show in place of the id. Keyed by the route and the id.
 */
export const useCrumbNames = defineStore('crumb-names', () => {
  const names = reactive<Record<string, string>>({})
  const key = (route: string, id: string) => `${route}:${id}`
  const set = (route: string, id: string, label: string) => {
    names[key(route, id)] = label
  }
  const get = (route: string, id: string) => names[key(route, id)]
  return { set, get }
})
