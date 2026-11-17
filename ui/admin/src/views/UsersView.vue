<script setup lang="ts">
import type { AdminUser } from 'backd-js'
import { PhDeviceMobile, PhMagnifyingGlass } from '@phosphor-icons/vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import TextField from '@/components/TextField.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { useRawAdmin } from '@/lib/adminRaw'
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

const PAGE = 25
const q = ref('')
const users = ref<AdminUser[]>([])
const cursor = ref<string | undefined>()
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')
let run = 0

async function load(more = false) {
  const mine = ++run
  loading.value = true
  error.value = ''
  try {
    const page = await admin.users.list({ limit: PAGE, q: q.value.trim() || undefined, after: more ? cursor.value : undefined })
    if (mine !== run) return
    users.value = more ? [...users.value, ...page.items] : page.items
    hasMore.value = page.has_more
    cursor.value = page.next_cursor
  } catch (e) {
    if (mine === run) error.value = errorText(e, t('users.loadFailed'))
  } finally {
    if (mine === run) loading.value = false
  }
}

// Typing searches after a short pause.
let timer: ReturnType<typeof setTimeout> | undefined
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void load(), 300)
})
onMounted(() => {
  void load()
  void loadMfa()
})

const canWrite = computed(() => session.canWrite('users'))
// The actions of a leak reach every account: the server needs full administration for them.
const isFull = computed(() => session.access?.level === 'full')

// Role and status filters apply to the pages loaded so far: the server only searches by email.
const roleFilter = ref('')
const statusFilter = ref('')
const roleOptions = computed(() => [...new Set(users.value.flatMap((u) => u.roles))].sort())
const stateOf = (u: AdminUser) => (u.erased_at ? 'erased' : u.disabled ? 'disabled' : !u.email_verified ? 'unverified' : 'active')
const shown = computed(() =>
  users.value.filter((u) => {
    if (roleFilter.value && !u.roles.includes(roleFilter.value)) return false
    const f = statusFilter.value
    if (!f) return true
    return f === 'expired' ? !!u.password_expired_at && !u.erased_at : stateOf(u) === f
  }),
)
const roleKind = (r: string) => (r === 'admin' ? 'violet' : r === 'staff' ? 'blue' : 'neutral')
const statusKind = (u: AdminUser) => ({ erased: 'neutral', disabled: 'error', unverified: 'warn', active: 'ok' })[stateOf(u)] as 'neutral' | 'error' | 'warn' | 'ok'

// Who still has to set up a second factor (the server lists the first ones).
const raw = useRawAdmin()
const mfaPending = ref(0)
const mfaIds = ref(new Set<string>())
async function loadMfa() {
  try {
    const m = await raw<{ enabled: boolean; total_without_mfa: number; users_without_mfa: { id: string }[] }>('GET', ['mfa'])
    if (!m.enabled) return
    mfaPending.value = m.total_without_mfa
    mfaIds.value = new Set(m.users_without_mfa.map((u) => u.id))
  } catch {
    // The level may not read it: the banner just doesn't show.
  }
}

const statusOf = (u: AdminUser) => (u.erased_at ? t('users.erased') : u.disabled ? t('users.disabled') : t('users.active'))

// Create
const creating = ref(false)
const newEmail = ref('')
const newPassword = ref('')
const createError = ref('')
const createBusy = ref(false)

function openCreate() {
  newEmail.value = ''
  newPassword.value = ''
  createError.value = ''
  creating.value = true
}

async function create() {
  createBusy.value = true
  createError.value = ''
  try {
    const user = await admin.users.create({ email: newEmail.value.trim(), password: newPassword.value || undefined })
    creating.value = false
    toasts.push(t('users.create.done', { email: user.email }))
    await router.push({ name: 'user', params: { realm: props.realm, id: user.id } })
  } catch (e) {
    createError.value = errorText(e, t('common.unexpected'))
  } finally {
    createBusy.value = false
  }
}
</script>

<template>
  <PageHeader :title="t('users.title')" :description="t('nav.desc.users')">
    <RouterLink v-if="canWrite && isFull" :to="{ name: 'security', params: { realm } }" class="inline-flex h-8 items-center rounded-md border border-slate-300 bg-white px-3 text-[13px] font-medium hover:bg-slate-100 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800" data-testid="passwords-toggle">{{ t('users.passwordsShow') }}</RouterLink>
    <AppButton v-if="canWrite" @click="openCreate">{{ t('users.create.open') }}</AppButton>
  </PageHeader>

  <RouterLink v-if="mfaPending > 0" :to="{ name: 'security', params: { realm } }" class="mb-4 flex items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-[13px] dark:border-amber-900 dark:bg-amber-950/40" data-testid="mfa-banner">
    <PhDeviceMobile :size="20" class="text-amber-700 dark:text-amber-400" aria-hidden="true" />
    <span class="flex-1">{{ t('users.mfaPending', { n: mfaPending }) }}</span>
    <span class="underline">{{ t('users.mfaSee') }}</span>
  </RouterLink>

  <AppAlert v-if="error" kind="error" class="mb-4">
    {{ error }}
    <button type="button" class="ml-2 underline" @click="load()">{{ t('common.retry') }}</button>
  </AppAlert>

  <AppCard flush>
    <form class="flex flex-wrap items-center gap-2 border-b border-slate-200 p-3 dark:border-slate-800" role="search" @submit.prevent="load()">
      <div class="relative">
        <PhMagnifyingGlass :size="14" class="pointer-events-none absolute left-2.5 top-2.5 text-slate-400" aria-hidden="true" />
        <input v-model="q" type="search" class="h-8 w-72 max-w-full rounded-md border border-slate-300 bg-white pl-8 pr-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :placeholder="t('users.search')" :aria-label="t('users.search')" autocomplete="off" />
      </div>
      <select v-model="roleFilter" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('users.roles')" data-testid="filter-role">
        <option value="">{{ t('users.anyRole') }}</option>
        <option v-for="r in roleOptions" :key="r" :value="r">{{ r }}</option>
      </select>
      <select v-model="statusFilter" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('users.status')" data-testid="filter-status">
        <option value="">{{ t('users.anyStatus') }}</option>
        <option value="active">{{ t('users.active') }}</option>
        <option value="unverified">{{ t('users.unverified') }}</option>
        <option value="disabled">{{ t('users.disabled') }}</option>
        <option value="expired">{{ t('users.passwordExpired') }}</option>
        <option value="erased">{{ t('users.erased') }}</option>
      </select>
      <span class="ml-auto text-xs text-slate-600 dark:text-slate-400" role="status">{{ loading ? t('common.loading') : t('users.shown', { shown: shown.length, loaded: users.length }) }}</span>
    </form>
    <table class="w-full text-left text-sm" data-testid="users-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('users.user') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.roles') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.security') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.status') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.created') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in shown" :key="u.id" class="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50" @click="router.push({ name: 'user', params: { realm, id: u.id } })">
          <td class="px-3 py-2">
            <div class="flex items-center gap-2.5">
              <UserAvatar :name="u.email" :muted="!!u.erased_at" />
              <RouterLink :to="{ name: 'user', params: { realm, id: u.id } }" class="font-medium hover:underline" @click.stop>{{ u.email }}</RouterLink>
            </div>
          </td>
          <td class="px-3 py-2">
            <span v-if="!u.roles.length" class="text-slate-500">{{ t('users.noRoles') }}</span>
            <span v-else class="flex flex-wrap gap-1"><AppBadge v-for="r in u.roles" :key="r" :kind="roleKind(r)">{{ r }}</AppBadge></span>
          </td>
          <td class="px-3 py-2 text-slate-600 dark:text-slate-400">
            <span v-if="mfaIds.has(u.id)" class="text-amber-800 dark:text-amber-300">{{ t('users.mfaMissing') }}</span>
            <span v-else-if="u.password_expired_at && !u.erased_at" data-testid="password-expired-tag">{{ t('users.passwordExpired') }}</span>
            <span v-else>—</span>
          </td>
          <td class="px-3 py-2"><AppBadge :kind="statusKind(u)">{{ statusOf(u) }}</AppBadge></td>
          <td class="px-3 py-2 text-slate-600 dark:text-slate-400">{{ formatDate(u.created_at) }}</td>
        </tr>
        <tr v-if="!shown.length && !loading">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('users.empty') }}</td>
        </tr>
      </tbody>
    </table>
    <div v-if="hasMore" class="border-t border-slate-200 p-3 dark:border-slate-800">
      <AppButton variant="secondary" :busy="loading" @click="load(true)">{{ t('common.loadMore') }}</AppButton>
    </div>
  </AppCard>

  <FormDialog v-model:open="creating" :title="t('users.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="newEmail" type="email" :label="t('users.email')" autocomplete="off" required />
    <TextField v-model="newPassword" type="password" :label="t('users.create.password')" :help="t('users.create.passwordHelp')" autocomplete="new-password" />
  </FormDialog>
</template>
