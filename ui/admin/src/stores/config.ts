import type { AdminConfig } from 'backd-js'
import { defineStore } from 'pinia'
import { computed, ref, shallowRef, watch } from 'vue'
import { errorText } from '@/lib/format'
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

  const error = ref('')
  const busy = ref(false)

  async function load(force = false) {
    if (!session.client || !session.canRead('config')) return
    if (!force && config.value && forRealm === session.realm) return
    busy.value = true
    error.value = ''
    loading ??= session.client.admin
      .config()
      .then((c) => {
        config.value = c
        forRealm = session.realm
      })
      .catch((e: unknown) => {
        error.value = errorText(e, '')
      })
      .finally(() => {
        loading = null
        busy.value = false
      })
    await loading
  }

  /** Whether the realm sends email (changing an address and emailing invitations need it); null: not known. */
  const sendsEmail = computed<boolean | null>(() => (config.value ? !!config.value.settings.email : null))
  /** The roles declared in realm.yaml, with the emails seeded into them (null: not shown to this level). */
  const roles = computed(() => (config.value?.settings.roles ?? null) as Record<string, { seeded_emails?: string[] }> | null)

  /** The realm's databases (null: not known). */
  const databases = computed(() => (config.value ? Object.keys(config.value.databases).sort() : null))

  /** A collection's schema and whether it keeps a trash, from the configuration (null: not known). */
  function collectionInfo(database: string, name: string): { schema: Record<string, unknown>; softDelete: boolean } | null {
    const dbs = config.value?.databases as Record<string, { collections?: { name: string; schema: Record<string, unknown>; policy?: { soft_delete?: unknown } }[] }> | undefined
    const c = dbs?.[database]?.collections?.find((x) => x.name === name)
    return c ? { schema: c.schema, softDelete: !!c.policy?.soft_delete } : null
  }

  function reset() {
    config.value = null
    error.value = ''
    forRealm = ''
  }
  // What one administrator may read is not for the next one.
  watch(() => session.signedIn, (signedIn) => {
    if (!signedIn) reset()
  })
  return { config, error, busy, sendsEmail, roles, databases, collectionInfo, load, reset }
})
