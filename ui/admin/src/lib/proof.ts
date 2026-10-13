import { computed, ref } from 'vue'
import { useSession } from '@/stores/session'

export type ProofMethod = 'totp' | 'recovery_code' | 'password' | 'email_code'

/**
 * The fresh proof of identity sensitive actions ask for (creating an API key as a signed-in
 * administrator): which proof the user gives, the value typed, and the call that makes it.
 * The client keeps the proof for a few minutes, so one prompt covers a few actions.
 */
export function useProof() {
  const session = useSession()
  const methods = ref<ProofMethod[]>([])
  const method = ref<ProofMethod | ''>('')
  const value = ref('')
  const loaded = ref(false)
  const codeSent = ref(false)

  /** Whether the dialog has to ask: no proof is held and the user has a way to give one. */
  const needed = computed(() => loaded.value && method.value !== '' && !fresh())

  function fresh(): boolean {
    return session.client?.auth.hasFreshProof() ?? false
  }

  /** Reads which proofs the user gives; call it when the dialog opens. */
  async function prepare() {
    value.value = ''
    codeSent.value = false
    try {
      const me = await session.client!.auth.me()
      methods.value = me.reauth_methods ?? []
      method.value = methods.value[0] ?? ''
    } catch {
      methods.value = []
      method.value = ''
    }
    loaded.value = true
  }

  /** Emails the code to a user who has no password and no second factor. */
  async function sendCode() {
    await session.client!.auth.requestReauthCode()
    codeSent.value = true
  }

  /** Gives the proof, when one is needed and not held yet. */
  async function prove() {
    if (!needed.value) return
    const m = method.value as ProofMethod
    await session.client!.auth.reauth(m === 'password' ? { method: m, password: value.value } : { method: m, code: value.value.trim() })
    value.value = ''
  }

  return { methods, method, value, needed, codeSent, prepare, sendCode, prove }
}
