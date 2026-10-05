import { createClient, ForbiddenError, type AdminAccess, type Client, type TokenStorage } from 'backd-js'
import { defineStore } from 'pinia'
import { computed, shallowRef, ref } from 'vue'
import { watchIdle, type IdleWatch } from '@/lib/idle'
import { DEFAULT_IDLE_SECONDS } from '@/lib/runtime-config'

export type EndReason = 'signed-out' | 'idle' | 'expired'

/** Admin areas, as `whoami` names them. */
export type Area = 'users' | 'invitations' | 'apikeys' | 'secrets' | 'audit' | 'functions' | 'data' | 'config'

export const AREAS: readonly Area[] = ['users', 'invitations', 'apikeys', 'secrets', 'audit', 'functions', 'data', 'config']

/** whoami's `all` stands for every area. */
export function expandAreas(names: readonly string[]): Area[] {
  if (names.includes('all')) return [...AREAS]
  return AREAS.filter((a) => names.includes(a))
}

const tabKey = (realm: string) => `backd-admin:${realm}`

function tabStorage(realm: string): TokenStorage {
  return {
    get: () => sessionStorage.getItem(tabKey(realm)),
    set: (t) => sessionStorage.setItem(tabKey(realm), t),
    remove: () => sessionStorage.removeItem(tabKey(realm)),
  }
}

function memoryStore(): TokenStorage {
  let token: string | null = null
  return { get: () => token, set: (t) => void (token = t), remove: () => void (token = null) }
}

/** Why a sign-in didn't end up as an administrator. */
export class NotAdminError extends Error {}

/**
 * The admin session: one realm at a time, the token in memory (or, if the
 * administrator opts in, in this tab's sessionStorage, never localStorage),
 * and what `whoami` says they may do.
 */
export const useSession = defineStore('session', () => {
  const realm = ref('')
  const client = shallowRef<Client | null>(null)
  const access = shallowRef<AdminAccess | null>(null)
  const ended = ref<EndReason | null>(null)
  const idleSeconds = ref(DEFAULT_IDLE_SECONDS)
  let idle: IdleWatch | null = null

  const signedIn = computed(() => access.value !== null)
  const email = computed(() => access.value?.user?.email ?? '')

  /** Whether the signed-in level may read an area (changing implies reading). */
  const writable = computed(() => expandAreas(access.value?.write ?? []))
  const readable = computed(() => expandAreas([...(access.value?.read ?? []), ...(access.value?.write ?? [])]))
  const canRead = (area: Area) => readable.value.includes(area)
  const canWrite = (area: Area) => writable.value.includes(area)

  function begin(forRealm: string, keepInTab: boolean) {
    realm.value = forRealm
    client.value = createClient({
      url: window.location.origin,
      realm: forRealm,
      storage: keepInTab ? tabStorage(forRealm) : memoryStore(),
    })
    // The server refusing the token (expired, revoked elsewhere, user disabled).
    client.value.auth.onAuthChange((event) => {
      if (event === 'SESSION_EXPIRED') expired()
    })
  }

  async function adopt(c: Client) {
    try {
      access.value = await c.admin.whoami()
    } catch (e) {
      // A valid session of someone with no admin role is no admin session.
      await c.auth.logout().catch(() => {})
      client.value = null
      if (e instanceof ForbiddenError) throw new NotAdminError()
      throw e
    }
    ended.value = null
    idle?.stop()
    idle = watchIdle(idleSeconds.value, () => void signOut('idle'))
  }

  async function signIn(forRealm: string, emailAddress: string, password: string, keepInTab: boolean) {
    begin(forRealm, keepInTab)
    const c = client.value!
    await c.auth.login({ email: emailAddress, password })
    await adopt(c)
  }

  /** After a reload in the same tab: the opted-in token is still there. */
  async function restore(forRealm: string): Promise<boolean> {
    if (signedIn.value && realm.value === forRealm) return true
    if (!sessionStorage.getItem(tabKey(forRealm))) return false
    begin(forRealm, true)
    const c = client.value!
    try {
      await adopt(c)
      return true
    } catch {
      client.value = null
      return false
    }
  }

  /** Ends the session on the server too (a revoke), then forgets everything. */
  async function signOut(reason: EndReason = 'signed-out') {
    idle?.stop()
    idle = null
    const c = client.value
    access.value = null
    client.value = null
    ended.value = reason
    if (c) await c.auth.logout().catch(() => {})
  }

  /** The server refused the token: it expired or was revoked elsewhere. */
  function expired() {
    if (signedIn.value) void signOut('expired')
  }

  function setIdleSeconds(n: number) {
    idleSeconds.value = n
  }

  return { realm, client, access, ended, signedIn, email, idleSeconds, readable, writable, canRead, canWrite, signIn, restore, signOut, expired, setIdleSeconds }
})
