<script setup lang="ts">
import type { AdminUser } from 'backd-js'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FormDialog from '@/components/FormDialog.vue'
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
onMounted(() => void load())

const canWrite = computed(() => session.canWrite('users'))

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
  <div class="flex flex-wrap items-end justify-between gap-4">
    <h1 class="text-2xl font-semibold">{{ t('users.title') }}</h1>
    <AppButton v-if="canWrite" @click="openCreate">{{ t('users.create.open') }}</AppButton>
  </div>

  <form class="mt-4 max-w-md" role="search" @submit.prevent="load()">
    <TextField v-model="q" type="search" :label="t('users.search')" :help="t('users.searchHelp')" autocomplete="off" />
  </form>

  <AppAlert v-if="error" kind="error" class="mt-4">
    {{ error }}
    <button type="button" class="ml-2 underline" @click="load()">{{ t('common.retry') }}</button>
  </AppAlert>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="users-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('users.email') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.status') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.roles') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('users.created') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in users" :key="u.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
          <td class="px-3 py-2">
            <RouterLink :to="{ name: 'user', params: { realm, id: u.id } }" class="font-medium underline">{{ u.email }}</RouterLink>
          </td>
          <td class="px-3 py-2">{{ statusOf(u) }}<span v-if="!u.email_verified && !u.erased_at"> · {{ t('users.unverified') }}</span></td>
          <td class="px-3 py-2">{{ u.roles.length ? u.roles.join(', ') : t('users.noRoles') }}</td>
          <td class="px-3 py-2">{{ formatDate(u.created_at) }}</td>
        </tr>
        <tr v-if="!users.length && !loading">
          <td colspan="4" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('users.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-if="loading" class="mt-3 text-sm text-slate-600 dark:text-slate-400" role="status">{{ t('common.loading') }}</p>
  <div v-if="hasMore" class="mt-3">
    <AppButton variant="secondary" :busy="loading" @click="load(true)">{{ t('common.loadMore') }}</AppButton>
  </div>

  <FormDialog v-model:open="creating" :title="t('users.create.title')" :submit="t('common.create')" :busy="createBusy" :error="createError" @submit="create">
    <TextField v-model="newEmail" type="email" :label="t('users.email')" autocomplete="off" required />
    <TextField v-model="newPassword" type="password" :label="t('users.create.password')" :help="t('users.create.passwordHelp')" autocomplete="new-password" />
  </FormDialog>
</template>
