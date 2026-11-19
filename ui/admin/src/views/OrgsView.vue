<script setup lang="ts">
import type { AdminOrg } from 'backd-js'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PhMagnifyingGlass } from '@phosphor-icons/vue'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
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

  <AppAlert v-if="error" kind="error" class="mb-4">
    {{ error }}
    <button type="button" class="ml-2 underline" @click="load()">{{ t('common.retry') }}</button>
  </AppAlert>

  <AppCard flush>
    <form class="flex flex-wrap items-center gap-2 border-b border-slate-200 p-3 dark:border-slate-800" role="search" @submit.prevent="load()">
      <div class="relative">
        <PhMagnifyingGlass :size="14" class="pointer-events-none absolute left-2.5 top-2.5 text-slate-400" aria-hidden="true" />
        <input v-model="q" type="search" class="h-8 w-72 max-w-full rounded-md border border-slate-300 bg-white pl-8 pr-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :placeholder="t('orgs.search')" :aria-label="t('orgs.search')" autocomplete="off" />
      </div>
      <select v-model="status" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('orgs.filter')">
        <option value="">{{ t('common.anyStatus') }}</option>
        <option value="active">{{ t('orgs.status.active') }}</option>
        <option value="orphaned">{{ t('orgs.status.orphaned') }}</option>
        <option value="deleted">{{ t('orgs.status.deleted') }}</option>
      </select>
      <span class="ml-auto text-xs text-slate-600 dark:text-slate-400" role="status">{{ loading ? t('common.loading') : t('orgs.count', { n: orgs.length }) }}</span>
    </form>
    <table class="w-full text-left text-sm" data-testid="orgs-table">
      <thead>
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('orgs.organization') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.membersCol') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.statusLabel') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.security') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('orgs.created') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="o in orgs" :key="o.id" class="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50" @click="router.push({ name: 'org', params: { realm, id: o.id } })">
          <td class="px-3 py-2">
            <div class="flex items-center gap-2.5">
              <span class="grid size-7 shrink-0 place-items-center rounded bg-slate-200 text-xs font-bold dark:bg-slate-700" aria-hidden="true">{{ o.name.slice(0, 1).toUpperCase() }}</span>
              <div>
                <RouterLink :to="{ name: 'org', params: { realm, id: o.id } }" class="font-medium hover:underline" @click.stop>{{ o.name }}</RouterLink>
                <div class="font-mono text-xs text-slate-500">{{ o.slug ?? '—' }}</div>
              </div>
            </div>
          </td>
          <td class="px-3 py-2">{{ o.members ?? 0 }}</td>
          <td class="px-3 py-2"><AppBadge :kind="o.status === 'active' ? 'ok' : o.status === 'orphaned' ? 'warn' : 'error'">{{ statusText(o.status) }}</AppBadge></td>
          <td class="px-3 py-2">
            <span class="flex flex-wrap gap-1">
              <AppBadge v-if="o.require_mfa">{{ t('orgs.requireMfa') }}</AppBadge>
              <AppBadge v-if="o.sso_required">{{ t('orgs.ssoRequired') }}</AppBadge>
              <span v-if="!o.require_mfa && !o.sso_required" class="text-slate-500">—</span>
            </span>
          </td>
          <td class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ formatDate(o.created_at) }}</td>
        </tr>
        <tr v-if="!orgs.length && !loading">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('orgs.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </AppCard>

  <FormDialog v-model:open="creating" :title="t('orgs.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="name" :label="t('orgs.name')" autocomplete="off" required />
    <TextField v-model="slug" :label="t('orgs.slug')" :help="t('orgs.create.slugHelp')" autocomplete="off" required />
    <TextField v-model="owner" type="email" :label="t('orgs.create.owner')" :help="t('orgs.create.ownerHelp')" autocomplete="off" required />
  </FormDialog>
</template>
