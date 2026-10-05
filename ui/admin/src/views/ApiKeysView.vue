<script setup lang="ts">
import type { APIKeyInfo } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate, parseList } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()

const canWrite = computed(() => session.canWrite('apikeys'))
const keys = ref<APIKeyInfo[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    keys.value = await admin.apiKeys.list()
  } catch (e) {
    error.value = errorText(e, t('apikeys.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

// Create: the key is shown once.
const creating = ref(false)
const name = ref('')
const role = ref<'data' | 'admin'>('data')
const expires = ref('')
const networks = ref('')
const scopes = ref('')
const createError = ref('')
const createBusy = ref(false)
const shownKey = ref('')
const keyOpen = computed({ get: () => shownKey.value !== '', set: (v) => { if (!v) shownKey.value = '' } })

function openCreate() {
  name.value = ''
  role.value = 'data'
  expires.value = ''
  networks.value = ''
  scopes.value = ''
  createError.value = ''
  creating.value = true
}
async function create() {
  createBusy.value = true
  createError.value = ''
  try {
    const made = await admin.apiKeys.create({
      name: name.value.trim(),
      role: role.value,
      expiresIn: expires.value.trim() || undefined,
      networks: parseList(networks.value),
      scopes: parseList(scopes.value),
    })
    creating.value = false
    shownKey.value = made.key
    toasts.push(t('apikeys.create.done', { name: made.name }))
    await load()
  } catch (e) {
    createError.value = errorText(e, t('common.unexpected'))
  } finally {
    createBusy.value = false
  }
}

// Revoke: the key's name is typed.
const revoking = ref(false)
const target = ref<APIKeyInfo>()
function askRevoke(k: APIKeyInfo) {
  target.value = k
  revoking.value = true
}
async function revoke() {
  const k = target.value!
  try {
    await admin.apiKeys.revoke(k.name)
    toasts.push(t('apikeys.revoke.done', { name: k.name }))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('apikeys.title') }}</h1>
    <AppButton v-if="canWrite" @click="openCreate">{{ t('apikeys.create.open') }}</AppButton>
  </div>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>
  <p v-if="loading" class="mt-4 text-sm" role="status">{{ t('common.loading') }}</p>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="apikeys-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.name') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.role') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.prefix') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.scopes') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.networks') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.expires') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('apikeys.lastUsed') }}</th>
          <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('common.revoke') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="k in keys" :key="k.name" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 font-medium">{{ k.name }}</td>
          <td class="px-3 py-2">{{ k.role }}</td>
          <td class="px-3 py-2 font-mono text-xs">{{ k.prefix }}…</td>
          <td class="px-3 py-2">{{ k.scopes.length ? k.scopes.join(', ') : t('apikeys.everything') }}</td>
          <td class="px-3 py-2">{{ k.networks.length ? k.networks.join(', ') : t('apikeys.anywhere') }}</td>
          <td class="px-3 py-2">{{ k.expires_at ? formatDate(k.expires_at) : t('apikeys.never') }}</td>
          <td class="px-3 py-2">{{ k.last_used_at ? formatDate(k.last_used_at) : t('common.never') }}</td>
          <td v-if="canWrite" class="px-3 py-2 text-right">
            <button type="button" class="underline" :aria-label="`${t('common.revoke')} ${k.name}`" @click="askRevoke(k)">{{ t('common.revoke') }}</button>
          </td>
        </tr>
        <tr v-if="!keys.length && !loading">
          <td colspan="8" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('apikeys.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <FormDialog v-model:open="creating" :title="t('apikeys.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="name" :label="t('apikeys.create.name')" :help="t('apikeys.create.nameHelp')" autocomplete="off" required />
    <div class="space-y-1">
      <label for="key-role" class="block text-sm font-medium">{{ t('apikeys.create.role') }}</label>
      <select id="key-role" v-model="role" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900" aria-describedby="key-role-help">
        <option value="data">{{ t('apikeys.roles.data') }}</option>
        <option value="admin">{{ t('apikeys.roles.admin') }}</option>
      </select>
      <p id="key-role-help" class="text-xs text-slate-600 dark:text-slate-400">{{ t('apikeys.create.roleHelp') }}</p>
    </div>
    <TextField v-model="expires" :label="t('apikeys.create.expires')" :help="t('apikeys.create.expiresHelp')" autocomplete="off" />
    <TextField v-model="networks" :label="t('apikeys.create.networks')" :help="t('apikeys.create.networksHelp')" autocomplete="off" />
    <TextField v-model="scopes" :label="t('apikeys.create.scopes')" :help="t('apikeys.create.scopesHelp')" autocomplete="off" />
  </FormDialog>

  <FormDialog v-model:open="keyOpen" :title="t('apikeys.create.keyTitle')" :description="t('apikeys.create.keyBody')" :submit="t('apikeys.create.close')" @submit="keyOpen = false">
    <TextField :model-value="shownKey" :label="t('apikeys.create.keyLabel')" autocomplete="off" readonly data-testid="api-key" />
  </FormDialog>

  <ConfirmDialog
    v-model:open="revoking"
    :title="t('apikeys.revoke.title', { name: target?.name ?? '' })"
    :description="t('apikeys.revoke.body')"
    :expected="target?.name ?? ''"
    :action="t('apikeys.revoke.action')"
    @confirm="revoke"
  />
</template>
