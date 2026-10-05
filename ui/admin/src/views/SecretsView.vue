<script setup lang="ts">
import type { SecretInfo } from 'backd-js'
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

const canWrite = computed(() => session.canWrite('secrets'))
const secrets = ref<SecretInfo[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    secrets.value = await admin.secrets.list()
  } catch (e) {
    error.value = errorText(e, t('secrets.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(() => {
  void load()
  void realmConfig.load()
})

const scopeName = (s: SecretInfo) => s.database || t('secrets.realmScope')

// Set: the value is write-only and leaves the form as soon as it is sent.
const setting = ref(false)
const name = ref('')
const value = ref('')
const database = ref('')
const setError = ref('')
const setBusy = ref(false)

function openSet() {
  name.value = ''
  value.value = ''
  database.value = ''
  setError.value = ''
  setting.value = true
}
async function set() {
  setBusy.value = true
  setError.value = ''
  try {
    await admin.secrets.set(name.value.trim(), value.value, { database: database.value.trim() || undefined })
    setting.value = false
    toasts.push(t('secrets.set.done', { name: name.value.trim() }))
    await load()
  } catch (e) {
    setError.value = errorText(e, t('common.unexpected'))
  } finally {
    value.value = ''
    setBusy.value = false
  }
}

// Delete: the secret's name is typed.
const deleting = ref(false)
const target = ref<SecretInfo>()
function askDelete(s: SecretInfo) {
  target.value = s
  deleting.value = true
}
async function remove() {
  const s = target.value!
  try {
    await admin.secrets.delete(s.name, { database: s.database || undefined })
    toasts.push(t('secrets.delete.done', { name: s.name }))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('secrets.title') }}</h1>
    <AppButton v-if="canWrite" @click="openSet">{{ t('secrets.set.open') }}</AppButton>
  </div>
  <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('secrets.help') }}</p>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>
  <p v-if="loading" class="mt-4 text-sm" role="status">{{ t('common.loading') }}</p>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="secrets-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('secrets.name') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('secrets.scope') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('secrets.updated') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('secrets.updatedBy') }}</th>
          <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('common.delete') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="s in secrets" :key="`${s.database}/${s.name}`" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 font-mono text-xs">{{ s.name }}</td>
          <td class="px-3 py-2">{{ scopeName(s) }}</td>
          <td class="px-3 py-2">{{ formatDate(s.updated_at) }}</td>
          <td class="px-3 py-2">{{ s.updated_by }}</td>
          <td v-if="canWrite" class="px-3 py-2 text-right">
            <button type="button" class="underline" :aria-label="`${t('common.delete')} ${s.name}`" @click="askDelete(s)">{{ t('common.delete') }}</button>
          </td>
        </tr>
        <tr v-if="!secrets.length && !loading">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('secrets.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <FormDialog v-model:open="setting" :title="t('secrets.set.title')" :submit="t('common.save')" :busy="setBusy" :error="setError" @submit="set">
    <TextField v-model="name" :label="t('secrets.set.name')" :help="t('secrets.set.nameHelp')" autocomplete="off" required />
    <TextField v-model="value" type="password" :label="t('secrets.set.value')" :help="t('secrets.set.valueHelp')" autocomplete="off" required />
    <div class="space-y-1">
      <label for="secret-scope" class="block text-sm font-medium">{{ t('secrets.set.scope') }}</label>
      <select v-if="realmConfig.databases" id="secret-scope" v-model="database" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="">{{ t('secrets.realmScope') }}</option>
        <option v-for="db in realmConfig.databases" :key="db" :value="db">{{ db }}</option>
      </select>
      <input v-else id="secret-scope" v-model="database" :placeholder="t('secrets.set.databaseName')" autocomplete="off" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900" />
    </div>
  </FormDialog>

  <ConfirmDialog
    v-model:open="deleting"
    :title="t('secrets.delete.title', { name: target?.name ?? '' })"
    :description="t('secrets.delete.body')"
    :expected="target?.name ?? ''"
    :action="t('secrets.delete.action')"
    @confirm="remove"
  />
</template>
