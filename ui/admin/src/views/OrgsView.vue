<script setup lang="ts">
import type { AdminOrg } from 'backd-js'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const router = useRouter()
const session = useSession()
const toasts = useToasts()

const canWrite = computed(() => session.canWrite('users'))
const q = ref('')
const status = ref<'' | 'active' | 'orphaned' | 'deleted'>('')
const orgs = ref<AdminOrg[]>([])
const loading = ref(false)
const error = ref('')
let run = 0

async function load() {
  const mine = ++run
  loading.value = true
  error.value = ''
  try {
    const items = await admin.orgs.list({ status: status.value || undefined, q: q.value.trim() || undefined })
    if (mine === run) orgs.value = items
  } catch (e) {
    if (mine === run) error.value = errorText(e, t('orgs.loadFailed'))
  } finally {
    if (mine === run) loading.value = false
  }
}
let timer: ReturnType<typeof setTimeout> | undefined
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void load(), 300)
})
watch(status, () => void load())
onMounted(() => void load())

const statusText = (s: AdminOrg['status']) => t(`orgs.status.${s}`)

// Create: the owner is a user of the realm, by email.
const creating = ref(false)
const name = ref('')
const slug = ref('')
const owner = ref('')
const createError = ref('')
const createBusy = ref(false)
function openCreate() {
  name.value = slug.value = owner.value = createError.value = ''
  creating.value = true
}
async function create() {
  createBusy.value = true
  createError.value = ''
  try {
    const user = await admin.users.find(owner.value.trim())
    if (!user) {
      createError.value = t('orgs.noSuchUser', { email: owner.value.trim() })
      return
    }
    const org = await admin.orgs.create({ name: name.value.trim(), slug: slug.value.trim(), owner: user.id })
    creating.value = false
    toasts.push(t('orgs.create.done', { name: org.name }))
    await router.push({ name: 'org', params: { realm: props.realm, id: org.id } })
  } catch (e) {
    createError.value = errorText(e, t('common.unexpected'))
  } finally {
    createBusy.value = false
  }
}
</script>

<template>
  <PageHeader :title="t('orgs.title')" :description="t('nav.desc.orgs')">
    <AppButton v-if="canWrite" @click="openCreate">{{ t('orgs.create.open') }}</AppButton>
  </PageHeader>

  <form class="mt-4 flex flex-wrap items-end gap-4" role="search" @submit.prevent="load()">
    <div class="w-full max-w-md">
      <TextField v-model="q" type="search" :label="t('orgs.search')" :help="t('orgs.searchHelp')" autocomplete="off" />
    </div>
    <div class="space-y-1">
      <label for="org-status" class="block text-sm font-medium">{{ t('orgs.filter') }}</label>
      <select id="org-status" v-model="status" class="block rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="">{{ t('common.anyStatus') }}</option>
        <option value="active">{{ t('orgs.status.active') }}</option>
        <option value="orphaned">{{ t('orgs.status.orphaned') }}</option>
        <option value="deleted">{{ t('orgs.status.deleted') }}</option>
      </select>
    </div>
  </form>

  <AppAlert v-if="error" kind="error" class="mt-4">
    {{ error }}
    <button type="button" class="ml-2 underline" @click="load()">{{ t('common.retry') }}</button>
  </AppAlert>

  <AppCard flush class="mt-4">
    <table class="w-full text-left text-sm" data-testid="orgs-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('orgs.name') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.slug') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.statusLabel') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.members') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.requireMfa') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.created') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="o in orgs" :key="o.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2">
            <RouterLink :to="{ name: 'org', params: { realm, id: o.id } }" class="font-medium underline">{{ o.name }}</RouterLink>
          </td>
          <td class="px-3 py-2 font-mono">{{ o.slug ?? '—' }}</td>
          <td class="px-3 py-2">{{ statusText(o.status) }}</td>
          <td class="px-3 py-2">{{ o.members ?? 0 }}</td>
          <td class="px-3 py-2">{{ o.require_mfa ? t('common.yes') : t('common.no') }}</td>
          <td class="px-3 py-2">{{ formatDate(o.created_at) }}</td>
        </tr>
        <tr v-if="!orgs.length && !loading">
          <td colspan="6" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('orgs.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </AppCard>
  <p v-if="loading" class="mt-3 text-sm text-slate-600 dark:text-slate-400" role="status">{{ t('common.loading') }}</p>

  <FormDialog v-model:open="creating" :title="t('orgs.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="name" :label="t('orgs.name')" autocomplete="off" required />
    <TextField v-model="slug" :label="t('orgs.slug')" :help="t('orgs.create.slugHelp')" autocomplete="off" required />
    <TextField v-model="owner" type="email" :label="t('orgs.create.owner')" :help="t('orgs.create.ownerHelp')" autocomplete="off" required />
  </FormDialog>
</template>
