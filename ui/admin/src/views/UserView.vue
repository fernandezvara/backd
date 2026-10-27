<script setup lang="ts">
import AppCard from '@/components/AppCard.vue'
import type { AdminUser, Identity, OwnedReport, UserSession } from 'backd-js'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useRealmConfig } from '@/stores/config'
import { useCrumbNames } from '@/stores/crumbs'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string; id: string }>()
const { t } = useI18n()
const admin = useAdmin()
const router = useRouter()
const session = useSession()
const crumbNames = useCrumbNames()
const toasts = useToasts()
const realmConfig = useRealmConfig()
const proof = useProof()

const user = ref<AdminUser>()
const loadError = ref('')
const canWrite = computed(() => session.canWrite('users'))
const erased = computed(() => !!user.value?.erased_at)

async function load() {
  loadError.value = ''
  try {
    user.value = await admin.users.get(props.id)
    crumbNames.set('user', props.id, user.value.email)
  } catch (e) {
    loadError.value = errorText(e, t('user.loadFailed'))
  }
}

/** Runs a change, reports it and refreshes; the server's refusal is shown as is. */
async function act(fn: () => Promise<unknown>, done: string) {
  try {
    await fn()
    toasts.push(done)
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

const toggleDisabled = () => {
  const u = user.value!
  return act(() => admin.users.update(u.id, { disabled: !u.disabled }), t(u.disabled ? 'user.enabledDone' : 'user.disabledDone', { email: u.email }))
}
const markVerified = () => act(() => admin.users.update(user.value!.id, { emailVerified: true }), t('user.verifiedDone', { email: user.value!.email }))

// A dialog with one field: the same shape for the password and the email.
function useFieldDialog(run: (value: string) => Promise<unknown>, done: (value: string) => string) {
  const open = ref(false)
  const value = ref('')
  const error = ref('')
  const busy = ref(false)
  async function submit() {
    busy.value = true
    error.value = ''
    try {
      await run(value.value)
      open.value = false
      toasts.push(done(value.value))
      await load()
    } catch (e) {
      error.value = errorText(e, t('common.unexpected'))
    } finally {
      busy.value = false
    }
  }
  return {
    open,
    value,
    error,
    busy,
    submit,
    show() {
      value.value = ''
      error.value = ''
      open.value = true
    },
  }
}
const password = useFieldDialog((v) => admin.users.setPassword(props.id, v), () => t('user.password.done'))
const expireReason = useFieldDialog((v) => admin.users.expirePassword(props.id, { reason: v.trim() || undefined }), () => t('user.expire.done'))

// A reset link: made with a fresh proof, shown once.
const linkOpen = ref(false)
const linkRedirect = ref('')
const linkBusy = ref(false)
const linkError = ref('')
const linkShown = ref<{ link: string; expiresAt: string }>()
const linkResult = ref(false)
// The link is shown once: it is not kept after the dialog closes.
watch(linkResult, (open) => {
  if (!open) linkShown.value = undefined
})
function showLink() {
  linkRedirect.value = ''
  linkError.value = ''
  linkOpen.value = true
  void proof.prepare()
}
async function makeLink() {
  linkBusy.value = true
  linkError.value = ''
  try {
    await proof.prove()
    linkShown.value = await admin.users.resetLink(props.id, { redirectTo: linkRedirect.value.trim() || undefined })
    linkOpen.value = false
    linkResult.value = true
  } catch (e) {
    linkError.value = errorText(e, t('common.unexpected'))
  } finally {
    linkBusy.value = false
  }
}
const email = useFieldDialog((v) => admin.users.changeEmail(props.id, v.trim()), (v) => t('user.email.done', { email: v.trim() }))

// Roles: the realm's declared roles and who is seeded in realm.yaml come from
// the configuration, which only a level with the config area can read.
const declared = computed(() => Object.keys(realmConfig.roles ?? {}).sort())
const seeded = computed(() => (realmConfig.roles ? Object.fromEntries(Object.entries(realmConfig.roles).map(([name, r]) => [name, r.seeded_emails ?? []])) : null))
const knowsSeeds = computed(() => seeded.value !== null)
const dbOnly = (role: string) => {
  const emails = seeded.value?.[role]
  return knowsSeeds.value && !(emails ?? []).some((e) => e.toLowerCase() === user.value?.email.toLowerCase())
}
const addable = computed(() => declared.value.filter((r) => !user.value?.roles.includes(r)))
const roleToAdd = ref('')
async function addRole() {
  const role = roleToAdd.value.trim()
  if (!role) return
  await act(() => admin.users.addRole(props.id, role), t('user.roles.added', { role }))
  roleToAdd.value = ''
}
const removeRole = (role: string) => act(() => admin.users.removeRole(props.id, role), t('user.roles.removed', { role }))

// Sign-in methods
const identities = ref<Identity[]>([])
const identitiesError = ref('')
async function loadIdentities() {
  identitiesError.value = ''
  try {
    identities.value = await admin.users.identities(props.id)
  } catch (e) {
    identitiesError.value = errorText(e, t('user.identities.loadFailed'))
  }
}
const unlinkOpen = ref(false)
const unlinking = ref('') // the provider being asked about; kept after the dialog closes, for the confirmation
function askUnlink(provider: string) {
  unlinking.value = provider
  unlinkOpen.value = true
}
async function unlink() {
  const provider = unlinking.value
  try {
    await admin.users.unlinkIdentity(props.id, provider)
    toasts.push(t('user.identities.unlinked', { provider }))
    await loadIdentities()
  } catch (e) {
    // The server's refusal (the last way in can't go) is shown as is.
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// Second factor
interface MfaState {
  available: boolean
  enabled: boolean
  methods: string[]
  recovery_codes_left: number
  required: boolean
}
const mfa = ref<MfaState>()
const mfaError = ref('')
async function loadMfa() {
  mfaError.value = ''
  try {
    mfa.value = await admin.users.mfa(props.id)
  } catch (e) {
    mfaError.value = errorText(e, t('user.mfa.loadFailed'))
  }
}
const resetting = ref(false)
async function resetMfa() {
  try {
    await admin.users.resetMfa(props.id)
    toasts.push(t('user.mfa.resetDone', { email: user.value?.email ?? '' }))
    await Promise.all([loadMfa(), loadSessions()])
  } catch (e) {
    // The server's refusal (not on yourself; none to reset) is shown as is.
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
// An administrator can't reset their own second factor: the server refuses, so the button isn't offered.
const isSelf = computed(() => session.email.toLowerCase() === user.value?.email.toLowerCase())

// Sessions
const sessions = ref<UserSession[]>([])
const sessionsError = ref('')
async function loadSessions() {
  sessionsError.value = ''
  try {
    sessions.value = await admin.users.sessions(props.id)
  } catch (e) {
    sessionsError.value = errorText(e, t('user.sessions.loadFailed'))
  }
}
async function revokeSession(id: string) {
  try {
    await admin.users.revokeSession(props.id, id)
    toasts.push(t('user.sessions.revoked'))
    await loadSessions()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// Push devices: only a realm with push: has any to show.
interface DeviceRow {
  id: string
  platform: string
  name: string | null
  last_seen: string
}
const devices = ref<DeviceRow[]>([])
const devicesError = ref('')
// Whether the realm has push: the server says so (404 push_not_configured), whatever areas this level reads.
const pushConfigured = ref(true)
async function loadDevices() {
  devicesError.value = ''
  try {
    devices.value = await admin.users.devices(props.id)
  } catch (e) {
    if ((e as { code?: string }).code === 'push_not_configured') pushConfigured.value = false
    else devicesError.value = errorText(e, t('user.devices.loadFailed'))
  }
}
async function removeDevice(id: string) {
  try {
    await admin.users.removeDevice(props.id, id)
    toasts.push(t('user.devices.removed'))
    await loadDevices()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// Erasing: what the user owns is shown first, then the user's email is typed.
const erasing = ref(false)
const report = ref<OwnedReport>()
const reportError = ref('')
const erasingBusy = ref(false)
watch(erasing, async (open) => {
  if (!open) return
  report.value = undefined
  reportError.value = ''
  try {
    report.value = await admin.users.owned(props.id)
  } catch (e) {
    reportError.value = errorText(e, t('user.erase.reportFailed'))
  }
})
async function erase() {
  const u = user.value!
  erasingBusy.value = true
  try {
    await admin.users.delete(u.id)
    toasts.push(t('user.erase.done', { email: u.email }))
    await router.push({ name: 'users', params: { realm: props.realm } })
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    erasingBusy.value = false
  }
}

const statusText = computed(() => {
  const base = erased.value ? t('users.erased') : user.value?.disabled ? t('users.disabled') : t('users.active')
  return user.value?.password_expired && !erased.value ? `${base} · ${t('users.passwordExpired')}` : base
})

onMounted(async () => {
  await load()
  if (user.value && !erased.value) {
    await Promise.all([loadSessions(), loadIdentities(), loadMfa(), realmConfig.load()])
    await loadDevices()
  }
})
</script>

<template>
  <RouterLink :to="{ name: 'users', params: { realm } }" class="text-sm underline">{{ t('user.back') }}</RouterLink>
  <AppAlert v-if="loadError" kind="error" class="mt-4">{{ loadError }}</AppAlert>

  <template v-if="user">
    <PageHeader :title="user.email" testid="user-email" />

    <dl class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('user.status') }}</dt>
        <dd class="mt-1" data-testid="user-status">{{ statusText }}</dd>
      </div>
      <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('user.emailVerified') }}</dt>
        <dd class="mt-1">{{ user.email_verified ? t('users.verified') : t('users.unverified') }}</dd>
      </div>
      <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('user.created') }}</dt>
        <dd class="mt-1">{{ formatDate(user.created_at) }}</dd>
      </div>
      <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('user.id') }}</dt>
        <dd class="mt-1 font-mono text-sm break-all">{{ user.id }}</dd>
      </div>
    </dl>

    <div v-if="canWrite && !erased" class="mt-6 flex flex-wrap gap-2" data-testid="user-actions">
      <AppButton variant="secondary" @click="toggleDisabled">{{ user.disabled ? t('user.enable') : t('user.disable') }}</AppButton>
      <AppButton v-if="!user.email_verified" variant="secondary" @click="markVerified">{{ t('user.verify') }}</AppButton>
      <AppButton variant="secondary" @click="password.show">{{ t('user.password.open') }}</AppButton>
      <AppButton variant="secondary" data-testid="expire-password" @click="expireReason.show">{{ t('user.expire.open') }}</AppButton>
      <AppButton v-if="realmConfig.sendsEmail !== false && !user.disabled" variant="secondary" data-testid="reset-link" @click="showLink">{{ t('user.resetLink.open') }}</AppButton>
      <AppButton v-if="realmConfig.sendsEmail !== false" variant="secondary" @click="email.show">{{ t('user.email.open') }}</AppButton>
      <AppButton variant="danger" @click="erasing = true">{{ t('user.erase.open') }}</AppButton>
    </div>

    <template v-if="!erased">
      <section class="mt-8" aria-labelledby="roles-h">
        <h2 id="roles-h" class="text-lg font-semibold">{{ t('user.roles.title') }}</h2>
        <p v-if="!user.roles.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('user.roles.none') }}</p>
        <ul class="mt-2 space-y-2" data-testid="user-roles">
          <li v-for="role in user.roles" :key="role" class="flex flex-wrap items-center gap-3 text-sm">
            <span class="rounded bg-slate-200 px-2 py-0.5 font-medium dark:bg-slate-700">{{ role }}</span>
            <span v-if="dbOnly(role)" class="text-amber-800 dark:text-amber-300" :title="t('user.roles.dbOnlyHelp')">⚠ {{ t('user.roles.dbOnly') }}</span>
            <button v-if="canWrite" type="button" class="underline" :aria-label="t('user.roles.remove', { role })" @click="removeRole(role)">{{ t('common.delete') }}</button>
          </li>
        </ul>
        <form v-if="canWrite" class="mt-3 flex max-w-md items-end gap-2" @submit.prevent="addRole">
          <div class="flex-1">
            <div v-if="declared.length" class="space-y-1">
              <label for="role-pick" class="block text-sm font-medium">{{ t('user.roles.pick') }}</label>
              <select id="role-pick" v-model="roleToAdd" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
                <option value="" disabled>—</option>
                <option v-for="r in addable" :key="r" :value="r">{{ r }}</option>
              </select>
            </div>
            <TextField v-else v-model="roleToAdd" :label="t('user.roles.pick')" :help="t('user.roles.unlisted')" autocomplete="off" />
          </div>
          <AppButton type="submit" variant="secondary">{{ t('user.roles.add') }}</AppButton>
        </form>
      </section>

      <section class="mt-8" aria-labelledby="networks-h">
        <h2 id="networks-h" class="text-lg font-semibold">{{ t('user.networks.title') }}</h2>
        <dl class="mt-2 grid max-w-xl gap-1 text-sm sm:grid-cols-[10rem_1fr]">
          <dt class="text-slate-600 dark:text-slate-400">{{ t('user.adminNetworks') }}</dt>
          <dd>{{ user.admin_networks.length ? user.admin_networks.join(', ') : t('user.anywhere') }}</dd>
          <dt class="text-slate-600 dark:text-slate-400">{{ t('user.loginNetworks') }}</dt>
          <dd>{{ user.login_networks.length ? user.login_networks.join(', ') : t('user.anywhere') }}</dd>
        </dl>
      </section>

      <section class="mt-8" aria-labelledby="identities-h">
        <h2 id="identities-h" class="text-lg font-semibold">{{ t('user.identities.title') }}</h2>
        <AppAlert v-if="identitiesError" kind="error" class="mt-2">{{ identitiesError }}</AppAlert>
        <AppCard v-else flush class="mt-2">
          <table class="w-full text-left text-sm" data-testid="user-identities">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('user.identities.provider') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.identities.email') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.identities.linked') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.identities.lastUsed') }}</th>
                <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('user.identities.unlink') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="i in identities" :key="i.provider" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="px-3 py-2 font-mono text-xs">{{ i.provider }}</td>
                <td class="px-3 py-2">{{ i.email }} <span class="text-xs text-slate-600 dark:text-slate-400">({{ i.email_verified ? t('user.identities.verified') : t('user.identities.unverified') }})</span></td>
                <td class="px-3 py-2">{{ formatDate(i.created_at) }}</td>
                <td class="px-3 py-2">{{ i.last_used_at ? formatDate(i.last_used_at) : t('user.identities.never') }}</td>
                <td v-if="canWrite" class="px-3 py-2 text-right">
                  <button type="button" class="underline" @click="askUnlink(i.provider)">{{ t('user.identities.unlink') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </AppCard>
      </section>

      <section class="mt-8" aria-labelledby="mfa-h">
        <h2 id="mfa-h" class="text-lg font-semibold">{{ t('user.mfa.title') }}</h2>
        <AppAlert v-if="mfaError" kind="error" class="mt-2">{{ mfaError }}</AppAlert>
        <div v-else-if="mfa" class="mt-2 flex flex-wrap items-center gap-4 text-sm" data-testid="user-mfa">
          <p data-testid="user-mfa-state">{{ mfa.enabled ? t('user.mfa.on', { left: mfa.recovery_codes_left }) : t('user.mfa.off') }}</p>
          <p v-if="mfa.required" class="text-slate-600 dark:text-slate-400">{{ t('user.mfa.required') }}</p>
          <AppButton v-if="canWrite && mfa.enabled && !isSelf" variant="danger" @click="resetting = true">{{ t('user.mfa.reset') }}</AppButton>
          <p v-else-if="mfa.enabled && isSelf" class="text-slate-600 dark:text-slate-400">{{ t('user.mfa.self') }}</p>
        </div>
      </section>

      <section class="mt-8" aria-labelledby="sessions-h">
        <h2 id="sessions-h" class="text-lg font-semibold">{{ t('user.sessions.title') }}</h2>
        <AppAlert v-if="sessionsError" kind="error" class="mt-2">{{ sessionsError }}</AppAlert>
        <p v-else-if="!sessions.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('user.sessions.none') }}</p>
        <AppCard v-else flush class="mt-2">
          <table class="w-full text-left text-sm" data-testid="user-sessions">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('user.sessions.created') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.sessions.lastUsed') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.sessions.expires') }}</th>
                <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('user.sessions.revoke') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in sessions" :key="s.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="px-3 py-2">{{ formatDate(s.created_at) }}</td>
                <td class="px-3 py-2">{{ formatDate(s.last_used_at) }}</td>
                <td class="px-3 py-2">{{ formatDate(s.expires_at) }}</td>
                <td v-if="canWrite" class="px-3 py-2 text-right">
                  <button type="button" class="underline" @click="revokeSession(s.id)">{{ t('user.sessions.revoke') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </AppCard>
      </section>
      <section v-if="pushConfigured" class="mt-8" aria-labelledby="devices-h">
        <h2 id="devices-h" class="text-lg font-semibold">{{ t('user.devices.title') }}</h2>
        <AppAlert v-if="devicesError" kind="error" class="mt-2">{{ devicesError }}</AppAlert>
        <p v-else-if="!devices.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400" data-testid="user-devices-none">{{ t('user.devices.none') }}</p>
        <AppCard v-else flush class="mt-2">
          <table class="w-full text-left text-sm" data-testid="user-devices">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('user.devices.platform') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.devices.name') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('user.devices.lastSeen') }}</th>
                <th v-if="canWrite" scope="col" class="px-3 py-2"><span class="sr-only">{{ t('user.devices.remove') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in devices" :key="d.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="px-3 py-2 font-mono text-xs">{{ d.platform }}</td>
                <td class="px-3 py-2">{{ d.name ?? '—' }}</td>
                <td class="px-3 py-2">{{ formatDate(d.last_seen) }}</td>
                <td v-if="canWrite" class="px-3 py-2 text-right">
                  <button type="button" class="underline" @click="removeDevice(d.id)">{{ t('user.devices.remove') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </AppCard>
      </section>
    </template>

    <FormDialog v-model:open="expireReason.open.value" :title="t('user.expire.title')" :description="t('user.expire.body')" :submit="t('user.expire.action')" :busy="expireReason.busy.value" :error="expireReason.error.value" @submit="expireReason.submit">
      <TextField v-model="expireReason.value.value" :label="t('user.expire.reason')" autocomplete="off" />
    </FormDialog>

    <FormDialog v-model:open="linkOpen" :title="t('user.resetLink.title')" :description="t('user.resetLink.body')" :submit="t('user.resetLink.action')" :busy="linkBusy" :error="linkError" @submit="makeLink">
      <TextField v-model="linkRedirect" :label="t('user.resetLink.redirect')" :help="t('user.resetLink.redirectHelp')" autocomplete="off" />
      <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
    </FormDialog>

    <FormDialog v-model:open="linkResult" :title="t('user.resetLink.shownTitle')" :description="t('user.resetLink.shownBody', { date: linkShown ? formatDate(linkShown.expiresAt) : '' })" :submit="t('user.resetLink.close')" @submit="linkResult = false">
      <TextField :model-value="linkShown?.link ?? ''" :label="t('user.resetLink.label')" autocomplete="off" readonly data-testid="reset-link-value" />
    </FormDialog>

    <FormDialog v-model:open="password.open.value" :title="t('user.password.title')" :submit="t('common.save')" :busy="password.busy.value" :error="password.error.value" @submit="password.submit">
      <TextField v-model="password.value.value" type="password" :label="t('user.password.label')" :help="t('user.password.help')" autocomplete="new-password" required />
    </FormDialog>
    <FormDialog v-model:open="email.open.value" :title="t('user.email.title')" :submit="t('common.save')" :busy="email.busy.value" :error="email.error.value" @submit="email.submit">
      <TextField v-model="email.value.value" type="email" :label="t('user.email.label')" :help="t('user.email.help')" autocomplete="off" required />
    </FormDialog>

    <ConfirmDialog v-model:open="resetting" :title="t('user.mfa.resetTitle', { email: user.email })" :description="t('user.mfa.resetBody')" :expected="user.email" :action="t('user.mfa.reset')" @confirm="resetMfa" />
    <ConfirmDialog v-model:open="unlinkOpen" :title="t('user.identities.unlinkTitle', { provider: unlinking })" :description="t('user.identities.unlinkBody')" :expected="unlinking" :action="t('user.identities.unlink')" @confirm="unlink" />
    <ConfirmDialog v-model:open="erasing" :title="t('user.erase.title', { email: user.email })" :description="t('user.erase.body')" :expected="user.email" :action="t('user.erase.action')" :busy="erasingBusy" @confirm="erase">
      <AppAlert v-if="reportError" kind="error">{{ reportError }}</AppAlert>
      <p v-else-if="!report" class="text-sm" role="status">{{ t('user.erase.loadingReport') }}</p>
      <div v-else class="space-y-2 text-sm" data-testid="owned-report">
        <ul v-if="report.collections.length" class="list-disc pl-5">
          <li v-for="c in report.collections" :key="`${c.database}/${c.collection}`">
            {{ t('user.erase.owned', { collection: `${c.database}/${c.collection}`, owned: c.owned, action: c.action ?? t('user.erase.policyNone') }) }}
          </li>
        </ul>
        <p v-else>{{ t('user.erase.none') }}</p>
        <p v-if="report.without_policy?.length">{{ t('user.erase.withoutPolicy', { names: report.without_policy.join(', ') }) }}</p>
        <p class="text-slate-600 dark:text-slate-400">{{ t('user.erase.deactivateHint') }}</p>
      </div>
    </ConfirmDialog>
  </template>
</template>
