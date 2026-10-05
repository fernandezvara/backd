<script setup lang="ts">
import type { Invitation } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const realmConfig = useRealmConfig()

const canWrite = computed(() => session.canWrite('invitations'))
const items = ref<Invitation[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    items.value = await admin.invitations.list()
  } catch (e) {
    error.value = errorText(e, t('invitations.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(() => {
  void load()
  void realmConfig.load()
})

// Create: with the token, which is shown once.
const creating = ref(false)
const createEmail = ref('')
const createExpires = ref('')
const createError = ref('')
const createBusy = ref(false)
const token = ref('')

function openCreate() {
  createEmail.value = ''
  createExpires.value = ''
  createError.value = ''
  creating.value = true
}
async function create() {
  createBusy.value = true
  createError.value = ''
  try {
    const made = await admin.invitations.create({ email: createEmail.value.trim() || undefined, expiresIn: createExpires.value.trim() || undefined })
    creating.value = false
    token.value = made.token
    toasts.push(t('invitations.create.done'))
    await load()
  } catch (e) {
    createError.value = errorText(e, t('common.unexpected'))
  } finally {
    createBusy.value = false
  }
}
const tokenOpen = computed({ get: () => token.value !== '', set: (v) => { if (!v) token.value = '' } })

// Send: backd emails it, nobody holds the token.
const sending = ref(false)
const sendEmail = ref('')
const sendError = ref('')
const sendBusy = ref(false)
function openSend() {
  sendEmail.value = ''
  sendError.value = ''
  sending.value = true
}
async function send() {
  sendBusy.value = true
  sendError.value = ''
  try {
    await admin.invitations.send({ email: sendEmail.value.trim() })
    sending.value = false
    toasts.push(t('invitations.send.done', { email: sendEmail.value.trim() }))
    await load()
  } catch (e) {
    sendError.value = errorText(e, t('common.unexpected'))
  } finally {
    sendBusy.value = false
  }
}

// Revoke, with the word typed.
const revoking = ref(false)
const target = ref<Invitation>()
function askRevoke(inv: Invitation) {
  target.value = inv
  revoking.value = true
}
async function revoke() {
  try {
    await admin.invitations.revoke(target.value!.id)
    toasts.push(t('invitations.revoke.done'))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('invitations.title') }}</h1>
    <div v-if="canWrite" class="flex gap-2">
      <AppButton v-if="realmConfig.sendsEmail !== false" variant="secondary" @click="openSend">{{ t('invitations.send.open') }}</AppButton>
      <AppButton @click="openCreate">{{ t('invitations.create.open') }}</AppButton>
    </div>
  </div>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>
  <p v-if="loading" class="mt-4 text-sm" role="status">{{ t('common.loading') }}</p>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="invitations-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('invitations.email') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('invitations.createdBy') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('invitations.created') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('invitations.expires') }}</th>
          <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('common.revoke') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="inv in items" :key="inv.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2">{{ inv.email ?? t('invitations.anyone') }}</td>
          <td class="px-3 py-2">{{ inv.created_by }}</td>
          <td class="px-3 py-2">{{ formatDate(inv.created_at) }}</td>
          <td class="px-3 py-2">{{ formatDate(inv.expires_at) }}</td>
          <td v-if="canWrite" class="px-3 py-2 text-right">
            <button type="button" class="underline" @click="askRevoke(inv)">{{ t('common.revoke') }}</button>
          </td>
        </tr>
        <tr v-if="!items.length && !loading">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('invitations.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <FormDialog v-model:open="creating" :title="t('invitations.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="createEmail" type="email" :label="t('invitations.create.email')" :help="t('invitations.create.emailHelp')" autocomplete="off" />
    <TextField v-model="createExpires" :label="t('invitations.create.expires')" :help="t('invitations.create.expiresHelp')" autocomplete="off" />
  </FormDialog>

  <FormDialog v-model:open="tokenOpen" :title="t('invitations.create.tokenTitle')" :description="t('invitations.create.tokenBody')" :submit="t('invitations.create.close')" @submit="tokenOpen = false">
    <TextField :model-value="token" :label="t('invitations.create.tokenLabel')" autocomplete="off" data-testid="invitation-token" />
  </FormDialog>

  <FormDialog v-model:open="sending" :title="t('invitations.send.title')" :submit="t('invitations.send.open')" :busy="sendBusy" :error="sendError" @submit="send">
    <TextField v-model="sendEmail" type="email" :label="t('invitations.send.email')" autocomplete="off" required />
  </FormDialog>

  <ConfirmDialog v-model:open="revoking" :title="t('invitations.revoke.title')" :expected="t('invitations.revoke.expected')" :action="t('invitations.revoke.action')" @confirm="revoke" />
</template>
