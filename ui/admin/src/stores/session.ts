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

interface MemoryStore extends TokenStorage {
  /** The token right now, synchronously (a closing page can't wait). */
  peek: () => string | null
}

function memoryStore(): MemoryStore {
  let token: string | null = null
  return { get: () => token, set: (t) => void (token = t), remove: () => void (token = null), peek: () => token }
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
  let memory: MemoryStore | null = null

  const signedIn = computed(() => access.value !== null)
  const email = computed(() => access.value?.user?.email ?? '')

  /** Whether the signed-in level may read an area (changing implies reading). */
  const writable = computed(() => expandAreas(access.value?.write ?? []))
  const readable = computed(() => expandAreas([...(access.value?.read ?? []), ...(access.value?.write ?? [])]))
  const canRead = (area: Area) => readable.value.includes(area)
  const canWrite = (area: Area) => writable.value.includes(area)

  function begin(forRealm: string, keepInTab: boolean) {
    realm.value = forRealm
    memory = keepInTab ? null : memoryStore()
    client.value = createClient({
      url: window.location.origin,
      realm: forRealm,
      storage: memory ?? tabStorage(forRealm),
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

  /** What the sign-in waits for after a right password: a code of the second factor, or setting one up. */
  const step = ref<{ kind: 'code' | 'enroll'; challenge: string } | null>(null)

  /** Signs in; resolves with what is still needed: nothing (`done`), a code, or an enrolment. */
  async function signIn(forRealm: string, emailAddress: string, password: string, keepInTab: boolean): Promise<'done' | 'code' | 'enroll'> {
    begin(forRealm, keepInTab)
    const c = client.value!
    const result = await c.auth.signIn({ email: emailAddress, password })
    if ('challenge' in result) {
      step.value = { kind: 'mfaRequired' in result ? 'code' : 'enroll', challenge: result.challenge }
      return step.value.kind
    }
    step.value = null
    await adopt(c)
    return 'done'
  }

  /** The second step: the code of the authenticator app, or a recovery code (xxxxx-xxxxx). */
  async function verifyCode(code: string) {
    const c = client.value!
    const trimmed = code.trim()
    await c.auth.verifyMfa(step.value!.challenge, trimmed.includes('-') ? 'recovery_code' : 'totp', trimmed)
    step.value = null
    await adopt(c)
  }

  /** Starts setting an authenticator app up for a sign-in whose role requires one. */
  function startEnrollment() {
    return client.value!.auth.mfa.setup({ challenge: step.value!.challenge })
  }

  /** Confirms the first code: the sign-in ends here, and the recovery codes come back to be shown. */
  async function confirmEnrollment(setupId: string, code: string): Promise<string[]> {
    const c = client.value!
    const done = await c.auth.mfa.confirm({ challenge: step.value!.challenge, setupId, code: code.trim() })
    step.value = null
    await adopt(c)
    return done.recovery_codes
  }

  /** Gives up a sign-in that waits for its second step. */
  function cancelStep() {
    step.value = null
    client.value = null
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

  /**
   * A page that closes or reloads loses a token that lives only in memory,
   * and the session would live on in the server until its idle timeout: end
   * it instead. (A tab that opted in keeps its token across a reload, so its
   * session is left alone.)
   */
  function revokeOnExit(event: PageTransitionEvent) {
    const token = memory?.peek()
    if (event.persisted || !token || !signedIn.value) return
    void fetch(`${window.location.origin}/v1/${encodeURIComponent(realm.value)}/_auth/logout`, {
      method: 'POST',
      keepalive: true,
      headers: { Authorization: `Bearer ${token}` },
    }).catch(() => {})
  }
  window.addEventListener('pagehide', revokeOnExit)

  function setIdleSeconds(n: number) {
    idleSeconds.value = n
  }

  return { realm, client, access, ended, signedIn, email, idleSeconds, readable, writable, canRead, canWrite, step, signIn, verifyCode, startEnrollment, confirmEnrollment, cancelStep, restore, signOut, expired, setIdleSeconds }
})
