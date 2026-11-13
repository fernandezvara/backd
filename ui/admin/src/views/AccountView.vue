<script setup lang="ts">
import type { Passkey } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { errorText, formatDate } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()
const toasts = useToasts()
const proof = useProof()

// Passkeys are made and used by the browser: a browser without them can list and remove, not add.
const canPasskey = typeof window.PublicKeyCredential !== 'undefined'
const passkeys = ref<Passkey[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    passkeys.value = await session.client!.auth.passkeys.list()
  } catch (e) {
    error.value = errorText(e, t('account.passkeys.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

/** A dismissed prompt says so; anything else is the server's own words. */
function failure(e: unknown): string {
  if (e instanceof Error && e.name === 'PasskeyError' && (e as { code?: string }).code === 'passkey_cancelled') return t('signIn.passkey.cancelled')
  return errorText(e, t('common.unexpected'))
}

// Add: the browser asks for the passkey after the fresh proof.
const adding = ref(false)
const name = ref('')
const addError = ref('')
const addBusy = ref(false)
function openAdd() {
  name.value = ''
  addError.value = ''
  adding.value = true
  void proof.prepare()
}
async function add() {
  addBusy.value = true
  addError.value = ''
  try {
    await proof.prove()
    await session.client!.auth.passkeys.register({ name: name.value.trim() || undefined })
    adding.value = false
    toasts.push(t('account.passkeys.added'))
    await load()
  } catch (e) {
    addError.value = failure(e)
  } finally {
    addBusy.value = false
  }
}

// Rename.
const renaming = ref<Passkey | null>(null)
const renameOpen = computed({ get: () => renaming.value !== null, set: (v) => { if (!v) renaming.value = null } })
const newName = ref('')
const renameError = ref('')
function askRename(p: Passkey) {
  renaming.value = p
  newName.value = p.name
  renameError.value = ''
}
async function rename() {
  renameError.value = ''
  try {
    await session.client!.auth.passkeys.rename(renaming.value!.id, newName.value.trim())
    renaming.value = null
    await load()
  } catch (e) {
    renameError.value = failure(e)
  }
}

// Remove: a fresh proof, and the server's refusal (the last way in cannot go) is shown as it is.
const removing = ref<Passkey | null>(null)
const removeOpen = computed({ get: () => removing.value !== null, set: (v) => { if (!v) removing.value = null } })
const removeError = ref('')
const removeBusy = ref(false)
function askRemove(p: Passkey) {
  removing.value = p
  removeError.value = ''
  void proof.prepare()
}
async function remove() {
  removeBusy.value = true
  removeError.value = ''
  try {
    await proof.prove()
    await session.client!.auth.passkeys.remove(removing.value!.id)
    toasts.push(t('account.passkeys.removed', { name: removing.value!.name }))
    removing.value = null
    // A passkey that opened this session ends it: the next request says so.
    await load()
  } catch (e) {
    removeError.value = failure(e)
  } finally {
    removeBusy.value = false
  }
}
</script>

<template>
  <PageHeader :title="t('account.title')" :description="t('nav.desc.account')" />

  <section class="mt-4" aria-labelledby="passkeys-h">
    <div class="flex flex-wrap items-center gap-3">
      <h2 id="passkeys-h" class="text-lg font-semibold">{{ t('account.passkeys.title') }}</h2>
      <AppButton v-if="canPasskey" class="ml-auto" data-testid="passkey-add" @click="openAdd">{{ t('account.passkeys.add') }}</AppButton>
    </div>
    <p class="mt-1 max-w-2xl text-sm text-slate-600 dark:text-slate-400">{{ t('account.passkeys.help') }}</p>
    <p v-if="!canPasskey" class="mt-2 text-sm">{{ t('account.passkeys.unsupported') }}</p>
    <AppAlert v-if="error" kind="error" class="mt-2">{{ error }}</AppAlert>
    <p v-if="loading" class="mt-2 text-sm" role="status">{{ t('common.loading') }}</p>
    <AppCard v-if="!error" flush class="mt-2">
      <table class="w-full text-left text-sm" data-testid="passkeys-table">
        <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
          <tr>
            <th scope="col" class="px-3 py-2">{{ t('account.passkeys.name') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('account.passkeys.kind') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('account.passkeys.created') }}</th>
            <th scope="col" class="px-3 py-2">{{ t('account.passkeys.lastUsed') }}</th>
            <th scope="col" class="px-3 py-2"><span class="sr-only">{{ t('account.passkeys.actions') }}</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in passkeys" :key="p.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800" data-testid="passkey-row">
            <td class="px-3 py-2 font-medium">{{ p.name }}</td>
            <td class="px-3 py-2">{{ p.backup_eligible ? t('account.passkeys.synced') : t('account.passkeys.device') }}</td>
            <td class="px-3 py-2">{{ formatDate(p.created_at) }}</td>
            <td class="px-3 py-2">{{ p.last_used_at ? formatDate(p.last_used_at) : t('common.never') }}</td>
            <td class="space-x-3 px-3 py-2 text-right">
              <button type="button" class="underline" :aria-label="`${t('account.passkeys.rename')} ${p.name}`" @click="askRename(p)">{{ t('account.passkeys.rename') }}</button>
              <button type="button" class="underline" :aria-label="`${t('common.delete')} ${p.name}`" @click="askRemove(p)">{{ t('common.delete') }}</button>
            </td>
          </tr>
          <tr v-if="!passkeys.length && !loading">
            <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('account.passkeys.empty') }}</td>
          </tr>
        </tbody>
      </table>
    </AppCard>
  </section>

  <FormDialog v-model:open="adding" :title="t('account.passkeys.addTitle')" :description="t('account.passkeys.addBody')" :submit="t('account.passkeys.addSubmit')" :busy="addBusy" :error="addError" @submit="add">
    <TextField v-model="name" :label="t('account.passkeys.nameLabel')" autocomplete="off" maxlength="64" data-testid="passkey-name" />
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>

  <FormDialog v-model:open="renameOpen" :title="t('account.passkeys.rename')" :submit="t('common.save')" :error="renameError" @submit="rename">
    <TextField v-model="newName" :label="t('account.passkeys.nameLabel')" autocomplete="off" maxlength="64" required data-testid="passkey-rename" />
  </FormDialog>

  <FormDialog v-model:open="removeOpen" :title="t('account.passkeys.removeTitle', { name: removing?.name ?? '' })" :description="t('account.passkeys.removeBody')" :submit="t('common.delete')" :busy="removeBusy" :error="removeError" @submit="remove">
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>
</template>
