<script setup lang="ts">
// The actions for a leak: expire every password, tell the users, end every session. They need
// full administration and a fresh proof of identity, which the dialogs ask for.
import type { PasswordState } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FormDialog from '@/components/FormDialog.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useRealmConfig } from '@/stores/config'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const toasts = useToasts()
const realmConfig = useRealmConfig()
const proof = useProof()

const state = ref<PasswordState>()
const loadError = ref('')
async function load() {
  loadError.value = ''
  try {
    state.value = await admin.passwords.state()
  } catch (e) {
    loadError.value = errorText(e, t('passwords.loadFailed'))
  }
}
onMounted(() => {
  void load()
  void realmConfig.load()
})

const canNotify = computed(() => realmConfig.sendsEmail !== false)
const epoch = computed(() => state.value?.epoch ?? null)

/** One dialog: the fields, the error, and the call. */
function useDialog(run: () => Promise<string>) {
  const open = ref(false)
  const busy = ref(false)
  const error = ref('')
  return {
    open,
    busy,
    error,
    async show(before?: () => void) {
      error.value = ''
      before?.()
      await load()
      void proof.prepare()
      open.value = true
    },
    async submit(check?: () => string) {
      error.value = check?.() ?? ''
      if (error.value) return
      busy.value = true
      try {
        await proof.prove()
        toasts.push(await run())
        open.value = false
        await load()
      } catch (e) {
        error.value = errorText(e, t('common.unexpected'))
      } finally {
        busy.value = false
      }
    },
  }
}

// Expire every password.
const reason = ref('')
const keepSessions = ref(false)
const notify = ref(false)
const typed = ref('')
const expire = useDialog(async () => {
  const done = await admin.passwords.expireAll({ reason: reason.value.trim() || undefined, keepSessions: keepSessions.value, notify: notify.value })
  return t('passwords.expire.done', { accounts: done.accounts, sessions: done.sessions })
})
const expectedCount = computed(() => String(state.value?.accounts ?? 0))
const showExpire = () =>
  expire.show(() => {
    reason.value = ''
    keepSessions.value = false
    notify.value = false
    typed.value = ''
  })
const submitExpire = () => expire.submit(() => (typed.value.trim() === expectedCount.value ? '' : t('passwords.expire.mismatch', { n: expectedCount.value })))

// Tell the users.
const tell = useDialog(async () => {
  await admin.passwords.notify()
  return t('passwords.tell.done')
})

// End every session.
const revokeTyped = ref('')
const revoke = useDialog(async () => {
  const done = await admin.sessions.revokeAll()
  return t('passwords.revoke.done', { sessions: done.sessions })
})
const showRevoke = () => revoke.show(() => (revokeTyped.value = ''))
const submitRevoke = () => revoke.submit(() => (revokeTyped.value.trim() === props.realm ? '' : t('passwords.revoke.mismatch', { realm: props.realm })))
</script>

<template>
  <section id="passwords-panel" class="mt-6 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" aria-labelledby="passwords-h" data-testid="passwords-panel">
    <h2 id="passwords-h" class="text-lg font-semibold">{{ t('passwords.title') }}</h2>
    <AppAlert v-if="loadError" kind="error" class="mt-2">{{ loadError }}</AppAlert>
    <p class="mt-1 text-sm text-slate-600 dark:text-slate-400" data-testid="passwords-status">
      <template v-if="state">
        {{ epoch ? t('passwords.expiredSince', { date: formatDate(epoch.valid_after), accounts: state.accounts }) : t('passwords.allValid', { accounts: state.accounts }) }}
        <template v-if="epoch?.reason"> · {{ epoch.reason }}</template>
      </template>
    </p>
    <div class="mt-3 flex flex-wrap gap-2">
      <AppButton variant="danger" @click="showExpire">{{ t('passwords.expire.open') }}</AppButton>
      <AppButton v-if="epoch && canNotify" variant="secondary" @click="tell.show()">{{ t('passwords.tell.open') }}</AppButton>
      <AppButton variant="secondary" @click="showRevoke">{{ t('passwords.revoke.open') }}</AppButton>
    </div>
  </section>

  <FormDialog v-model:open="expire.open.value" :title="t('passwords.expire.title')" :description="t('passwords.expire.body', { accounts: state?.accounts ?? 0 })" :submit="t('passwords.expire.action')" :busy="expire.busy.value" :error="expire.error.value" @submit="submitExpire">
    <TextField v-model="reason" :label="t('passwords.expire.reason')" :help="t('passwords.expire.reasonHelp')" autocomplete="off" />
    <label class="flex items-start gap-2 text-sm">
      <input v-model="keepSessions" type="checkbox" class="mt-1" data-testid="keep-sessions" />
      <span>{{ t('passwords.expire.keepSessions') }}</span>
    </label>
    <label v-if="canNotify" class="flex items-start gap-2 text-sm">
      <input v-model="notify" type="checkbox" class="mt-1" data-testid="notify-users" />
      <span>{{ t('passwords.expire.notify') }}</span>
    </label>
    <TextField v-model="typed" :label="t('passwords.expire.type', { n: expectedCount })" autocomplete="off" required />
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>

  <FormDialog v-model:open="tell.open.value" :title="t('passwords.tell.title')" :description="t('passwords.tell.body')" :submit="t('passwords.tell.action')" :busy="tell.busy.value" :error="tell.error.value" @submit="tell.submit()">
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>

  <FormDialog v-model:open="revoke.open.value" :title="t('passwords.revoke.title')" :description="t('passwords.revoke.body')" :submit="t('passwords.revoke.action')" :busy="revoke.busy.value" :error="revoke.error.value" @submit="submitRevoke">
    <TextField v-model="revokeTyped" :label="t('passwords.revoke.type', { realm })" autocomplete="off" required />
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>
</template>
