import type { AdminConfig } from 'backd-js'
import { defineStore } from 'pinia'
import { computed, shallowRef, watch } from 'vue'
import { useSession } from './session'

/**
 * The realm's configuration, read once per sign-in by the views that need a
 * fact from it. Only a level with the `config` area can read it; for the
 * others every fact is unknown (null) and the views fall back to offering
 * the control, the server being the judge.
 */
export const useRealmConfig = defineStore('realm-config', () => {
  const session = useSession()
  const config = shallowRef<AdminConfig | null>(null)
  let forRealm = ''
  let loading: Promise<void> | null = null

  async function load() {
    if (!session.client || !session.canRead('config')) return
    if (config.value && forRealm === session.realm) return
    loading ??= session.client.admin
      .config()
      .then((c) => {
        config.value = c
        forRealm = session.realm
      })
      .catch(() => {})
      .finally(() => {
        loading = null
      })
    await loading
  }

  /** Whether the realm sends email (changing an address and emailing invitations need it); null: not known. */
  const sendsEmail = computed<boolean | null>(() => (config.value ? !!config.value.settings.email : null))
  /** The roles declared in realm.yaml, with the emails seeded into them (null: not shown to this level). */
  const roles = computed(() => (config.value?.settings.roles ?? null) as Record<string, { seeded_emails?: string[] }> | null)

  function reset() {
    config.value = null
    forRealm = ''
  }
  // What one administrator may read is not for the next one.
  watch(() => session.signedIn, (signedIn) => {
    if (!signedIn) reset()
  })
  return { config, sendsEmail, roles, load, reset }
})
