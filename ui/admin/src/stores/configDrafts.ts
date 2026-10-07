import type { ConfigCheck } from 'backd-js'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { errorText } from '@/lib/format'
import { useSession } from './session'

/**
 * The configuration files being prepared (by path in the realm), and what the server says
 * about them together: each change is checked against the realm's whole configuration, with
 * all the drafts in place, so one draft can be wrong because of another.
 */
export const useConfigDrafts = defineStore('config-drafts', () => {
  const session = useSession()
  const texts = ref<Record<string, string>>({})
  const result = ref<ConfigCheck | null>(null)
  const checking = ref(false)
  const unavailable = ref('')
  let timer: ReturnType<typeof setTimeout> | undefined
  let seq = 0

  const paths = computed(() => Object.keys(texts.value))

  /** Records a draft (or forgets it when it is back to what the server runs) and checks again soon. */
  function set(path: string, text: string, initial: string) {
    if (text === initial) delete texts.value[path]
    else texts.value[path] = text
    clearTimeout(timer)
    if (!paths.value.length) {
      seq++
      result.value = null
      checking.value = false
      unavailable.value = ''
      return
    }
    checking.value = true
    timer = setTimeout(run, 500)
  }

  async function run() {
    const mine = ++seq
    if (!session.client) return
    checking.value = true
    try {
      const out = await session.client.admin.checkConfig({ ...texts.value })
      if (mine !== seq) return
      result.value = out
      unavailable.value = ''
    } catch (e) {
      if (mine !== seq) return
      result.value = null
      unavailable.value = errorText(e, '')
    } finally {
      if (mine === seq) checking.value = false
    }
  }

  function reset() {
    clearTimeout(timer)
    seq++
    texts.value = {}
    result.value = null
    checking.value = false
    unavailable.value = ''
  }
  return { texts, paths, result, checking, unavailable, set, reset }
})
