<script setup lang="ts">
import AppCard from '@/components/AppCard.vue'
import type { AdminUser, Identity, OwnedReport, Passkey, UserSession } from 'backd-js'
import { PhDeviceMobile, PhDeviceMobileCamera, PhFingerprint, PhKey, PhX } from '@phosphor-icons/vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import ActionMenu, { type MenuItem } from '@/components/ActionMenu.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import PrivacyPanel from '@/components/PrivacyPanel.vue'
import SubTabs from '@/components/SubTabs.vue'
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
const unlinkingOrg = ref('')
function askUnlink(provider: string, org = '') {
  unlinkingOrg.value = org
  unlinking.value = provider
  unlinkOpen.value = true
}
async function unlink() {
  const provider = unlinking.value
  try {
    await admin.users.unlinkIdentity(props.id, provider, unlinkingOrg.value ? { org: unlinkingOrg.value } : undefined)
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
  passkeys?: number
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
// Passkeys: the list, to remove the one of a lost device.
const passkeys = ref<Passkey[]>([])
const passkeysError = ref('')
async function loadPasskeys() {
  passkeysError.value = ''
  try {
    passkeys.value = await admin.users.passkeys(props.id)
  } catch (e) {
    passkeysError.value = errorText(e, t('user.passkeys.loadFailed'))
  }
}
const removingPasskey = ref<Passkey | null>(null)
const removePasskeyOpen = ref(false)
function askRemovePasskey(p: Passkey) {
  removingPasskey.value = p
  removePasskeyOpen.value = true
}
async function removePasskey() {
  const p = removingPasskey.value
  if (!p) return
  try {
    await admin.users.removePasskey(props.id, p.id)
    toasts.push(t('user.passkeys.removed', { name: p.name }))
    await Promise.all([loadPasskeys(), loadMfa(), loadSessions()])
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
const resetting = ref(false)
async function resetMfa() {
  try {
    await admin.users.resetMfa(props.id)
    toasts.push(t('user.mfa.resetDone', { email: user.value?.email ?? '' }))
    await Promise.all([loadMfa(), loadPasskeys(), loadSessions()])
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

const roleKind = (r: string) => (r === 'admin' ? 'violet' : r === 'staff' ? 'blue' : 'neutral')
const methodsText = computed(() => [...new Set(identities.value.map((i) => i.provider))].join(', ') || '—')

// Every session of the user, ended one by one (the server has no single call for it).
async function revokeAll() {
  try {
    await Promise.all(sessions.value.map((s) => admin.users.revokeSession(props.id, s.id)))
    toasts.push(t('user.sessions.revokedAll'))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
  await loadSessions()
}

const menu = computed<MenuItem[]>(() => {
  const u = user.value
  if (!u) return []
  return [
    { id: 'toggle', label: u.disabled ? t('user.enable') : t('user.disable'), testid: 'menu-toggle' },
    { id: 'verify', label: t('user.verify'), hidden: u.email_verified },
    { id: 'password', label: t('user.password.open'), testid: 'menu-password' },
    { id: 'expire', label: t('user.expire.open'), testid: 'expire-password' },
    { id: 'email', label: t('user.email.open'), hidden: realmConfig.sendsEmail === false },
    { id: 'signout', label: t('user.signOutAll'), hidden: !sessions.value.length },
    { id: 'erase', label: t('user.erase.open'), danger: true, testid: 'menu-erase' },
  ]
})
function onMenu(id: string) {
  if (id === 'toggle') void toggleDisabled()
  else if (id === 'verify') void markVerified()
  else if (id === 'password') password.show()
  else if (id === 'expire') expireReason.show()
  else if (id === 'email') email.show()
  else if (id === 'signout') void revokeAll()
  else if (id === 'erase') erasing.value = true
}

const tab = ref('overview')
const statusText = computed(() => {
  const base = erased.value ? t('users.erased') : user.value?.disabled ? t('users.disabled') : t('users.active')
  return user.value?.password_expired && !erased.value ? `${base} · ${t('users.passwordExpired')}` : base
})

onMounted(async () => {
  await load()
  if (user.value && !erased.value) {
    await Promise.all([loadSessions(), loadIdentities(), loadMfa(), loadPasskeys(), realmConfig.load()])
    await loadDevices()
  }
})
</script>

<template>
  <RouterLink :to="{ name: 'users', params: { realm } }" class="text-sm underline">{{ t('user.back') }}</RouterLink>
  <AppAlert v-if="loadError" kind="error" class="mt-4">{{ loadError }}</AppAlert>

  <template v-if="user">
    <div class="mb-5 mt-3 flex flex-wrap items-center gap-4">
      <UserAvatar :name="user.email" size="lg" :muted="erased" />
      <div class="min-w-0">
        <h1 class="break-all text-xl font-semibold" data-testid="user-email">{{ user.email }}</h1>
        <p class="text-sm text-slate-600 dark:text-slate-400"><span class="font-mono">{{ user.id }}</span> · {{ t('user.joined', { date: formatDate(user.created_at) }) }}</p>
        <div class="mt-1.5 flex flex-wrap gap-1.5">
          <AppBadge :kind="erased ? 'neutral' : user.disabled ? 'error' : 'ok'"><span data-testid="user-status">{{ statusText }}</span></AppBadge>
          <AppBadge v-if="!erased && !user.email_verified" kind="warn">{{ t('users.unverified') }}</AppBadge>
          <AppBadge v-for="r in user.roles" :key="r" :kind="roleKind(r)">{{ r }}</AppBadge>
          <AppBadge v-if="mfa?.enabled || mfa?.passkeys">{{ t('user.mfa.badge') }}</AppBadge>
        </div>
      </div>
      <div v-if="canWrite && !erased" class="ml-auto flex items-center gap-2" data-testid="user-actions">
        <AppButton v-if="realmConfig.sendsEmail !== false && !user.disabled" variant="secondary" data-testid="reset-link" @click="showLink">{{ t('user.resetLink.open') }}</AppButton>
        <ActionMenu :label="t('user.moreActions')" :items="menu" @select="onMenu" />
      </div>
    </div>

    <SubTabs v-if="!erased" v-model="tab" :label="t('userTabs.label')" :tabs="[{ id: 'overview', label: t('userTabs.overview') }, { id: 'security', label: t('userTabs.security') }, { id: 'sessions', label: `${t('userTabs.sessions')}${sessions.length ? ` (${sessions.length})` : ''}` }, { id: 'privacy', label: t('userTabs.privacy') }]" />
    <template v-if="!erased">
      <div v-show="tab === 'overview'" class="grid gap-5 lg:grid-cols-3">
        <section class="lg:col-span-2" aria-labelledby="roles-h">
          <AppCard>
            <h2 id="roles-h" class="mb-3 text-sm font-semibold">{{ t('user.roles.title') }}</h2>
            <p v-if="!user.roles.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('user.roles.none') }}</p>
            <ul class="flex flex-wrap items-center gap-2" data-testid="user-roles">
              <li v-for="role in user.roles" :key="role" class="flex items-center gap-1.5">
                <AppBadge :kind="roleKind(role)">
                  {{ role }}
                  <button v-if="canWrite" type="button" class="ml-1 hover:text-red-600" :aria-label="t('user.roles.remove', { role })" @click="removeRole(role)"><PhX :size="10" weight="bold" aria-hidden="true" /></button>
                </AppBadge>
                <span v-if="dbOnly(role)" class="text-xs text-amber-800 dark:text-amber-300" :title="t('user.roles.dbOnlyHelp')">⚠ {{ t('user.roles.dbOnly') }}</span>
              </li>
            </ul>
            <form v-if="canWrite" class="mt-4 flex max-w-md items-end gap-2" @submit.prevent="addRole">
              <div class="flex-1">
                <div v-if="declared.length" class="space-y-1">
                  <label for="role-pick" class="block text-sm font-medium">{{ t('user.roles.pick') }}</label>
                  <select id="role-pick" v-model="roleToAdd" class="block h-8 w-full rounded-md border border-slate-300 bg-white px-3 text-[13px] dark:border-slate-600 dark:bg-slate-900">
                    <option value="" disabled>—</option>
                    <option v-for="r in addable" :key="r" :value="r">{{ r }}</option>
                  </select>
                </div>
                <TextField v-else v-model="roleToAdd" :label="t('user.roles.pick')" :help="t('user.roles.unlisted')" autocomplete="off" />
              </div>
              <AppButton type="submit" variant="secondary">{{ t('user.roles.add') }}</AppButton>
            </form>
          </AppCard>
        </section>
        <section aria-labelledby="facts-h">
          <AppCard flush>
            <h2 id="facts-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.facts') }}</h2>
            <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.emailVerified') }}</dt><dd class="flex items-center gap-2">{{ user.email_verified ? t('users.verified') : t('users.unverified') }}<button v-if="canWrite && !user.email_verified" type="button" class="underline" @click="markVerified">{{ t('user.verify') }}</button></dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.locale') }}</dt><dd>{{ user.locale || '—' }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.created') }}</dt><dd>{{ formatDate(user.created_at) }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.updated') }}</dt><dd>{{ formatDate(user.updated_at) }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.identities.title') }}</dt><dd>{{ methodsText }}</dd></div>
            </dl>
          </AppCard>
        </section>
      </div>

      <PrivacyPanel v-if="tab === 'privacy'" :realm="realm" :id="id" />
      <section v-if="tab === 'privacy' && canWrite" class="mt-5" aria-labelledby="erase-h">
        <div class="card-soft flex flex-wrap items-center gap-3 rounded-lg border border-red-200 bg-white p-4 dark:border-red-900 dark:bg-slate-900">
          <div class="flex-1">
            <h2 id="erase-h" class="text-sm font-semibold text-red-700 dark:text-red-400">{{ t('user.erase.open') }}</h2>
            <p class="text-[13px] text-slate-600 dark:text-slate-400">{{ t('user.erase.cardBody') }}</p>
          </div>
          <AppButton variant="danger" @click="erasing = true">{{ t('user.erase.open') }}</AppButton>
        </div>
      </section>

      <div v-show="tab === 'security'" class="grid gap-5 lg:grid-cols-2">
        <section aria-labelledby="identities-h">
          <AppCard flush>
            <h2 id="identities-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.identities.title') }}</h2>
            <AppAlert v-if="identitiesError" kind="error" class="m-3">{{ identitiesError }}</AppAlert>
            <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800" data-testid="user-identities">
              <li v-for="i in identities" :key="`${i.org ?? ''}/${i.provider}`" :data-testid="`identity-${i.provider}`" class="flex items-center gap-3 p-4 text-[13px]">
                <PhKey :size="20" class="text-slate-400" aria-hidden="true" />
                <div class="min-w-0 flex-1">
                  <b class="font-mono text-xs">{{ i.provider }}</b>
                  <div class="text-xs text-slate-600 dark:text-slate-400">{{ i.email }} ({{ i.email_verified ? t('user.identities.verified') : t('user.identities.unverified') }}) · {{ t('user.identities.linked') }} {{ formatDate(i.created_at) }} · {{ t('user.identities.lastUsed') }} {{ i.last_used_at ? formatDate(i.last_used_at) : t('user.identities.never') }}</div>
                  <div v-if="i.org" class="text-xs text-slate-600 dark:text-slate-400" data-testid="identity-org">{{ t('user.identities.viaOrg', { org: i.org }) }}</div>
                  <div v-if="i.dormant" class="text-xs text-amber-800 dark:text-amber-300" data-testid="identity-dormant">{{ t('user.identities.dormant') }}</div>
                </div>
                <AppButton v-if="canWrite" variant="danger" @click="askUnlink(i.provider, i.org)">{{ t('user.identities.unlink') }}</AppButton>
              </li>
              <li v-if="!identities.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('user.identities.none') }}</li>
            </ul>
            <p class="border-t border-slate-200 p-3 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-400">{{ t('user.identities.lastWayHint') }}</p>
          </AppCard>
        </section>

        <section aria-labelledby="passkeys-h">
          <AppCard flush>
            <h2 id="passkeys-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.passkeys.title') }}</h2>
            <AppAlert v-if="passkeysError" kind="error" class="m-3">{{ passkeysError }}</AppAlert>
            <p v-else-if="!passkeys.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400" data-testid="user-passkeys-none">{{ t('user.passkeys.none') }}</p>
            <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800" data-testid="user-passkeys">
              <li v-for="p in passkeys" :key="p.id" class="flex items-center gap-3 p-4 text-[13px]">
                <PhFingerprint :size="20" class="text-slate-400" aria-hidden="true" />
                <div class="flex-1"><b>{{ p.name }}</b><div class="text-xs text-slate-600 dark:text-slate-400">{{ t('user.passkeys.added') }} {{ formatDate(p.created_at) }} · {{ t('user.passkeys.lastUsed') }} {{ p.last_used_at ? formatDate(p.last_used_at) : t('common.never') }}</div></div>
                <AppButton v-if="canWrite" variant="danger" :aria-label="`${t('user.passkeys.remove')} ${p.name}`" @click="askRemovePasskey(p)">{{ t('user.passkeys.remove') }}</AppButton>
              </li>
            </ul>
          </AppCard>
        </section>

        <section aria-labelledby="mfa-h">
          <AppCard flush>
            <h2 id="mfa-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.mfa.title') }}</h2>
            <AppAlert v-if="mfaError" kind="error" class="m-3">{{ mfaError }}</AppAlert>
            <div v-else-if="mfa" class="flex flex-wrap items-center gap-3 p-4 text-[13px]" data-testid="user-mfa">
              <PhDeviceMobile :size="20" class="text-slate-400" aria-hidden="true" />
              <div class="flex-1">
                <p data-testid="user-mfa-state">{{ mfa.enabled ? t('user.mfa.on', { left: mfa.recovery_codes_left }) : t('user.mfa.off') }}</p>
                <p v-if="mfa.required" class="text-xs text-slate-600 dark:text-slate-400">{{ t('user.mfa.required') }}</p>
                <p v-if="(mfa.enabled || mfa.passkeys) && isSelf" class="text-xs text-slate-600 dark:text-slate-400">{{ t('user.mfa.self') }}</p>
              </div>
              <AppButton v-if="canWrite && (mfa.enabled || mfa.passkeys) && !isSelf" variant="danger" @click="resetting = true">{{ t('user.mfa.reset') }}</AppButton>
            </div>
          </AppCard>
        </section>

        <section aria-labelledby="networks-h">
          <AppCard flush>
            <h2 id="networks-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.networks.title') }}</h2>
            <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.adminNetworks') }}</dt><dd class="font-mono text-xs">{{ user.admin_networks.length ? user.admin_networks.join(', ') : t('user.anywhere') }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('user.loginNetworks') }}</dt><dd class="font-mono text-xs">{{ user.login_networks.length ? user.login_networks.join(', ') : t('user.anywhere') }}</dd></div>
            </dl>
          </AppCard>
        </section>
      </div>

      <div v-show="tab === 'sessions'" class="space-y-5">
        <section aria-labelledby="sessions-h">
          <AppCard flush>
            <div class="flex items-center border-b border-slate-200 px-4 py-2.5 dark:border-slate-800">
              <h2 id="sessions-h" class="text-sm font-semibold">{{ t('user.sessions.title') }}</h2>
              <AppButton v-if="canWrite && sessions.length" variant="danger" class="ml-auto" data-testid="sign-out-all" @click="revokeAll">{{ t('user.signOutAll') }}</AppButton>
            </div>
            <AppAlert v-if="sessionsError" kind="error" class="m-3">{{ sessionsError }}</AppAlert>
            <p v-else-if="!sessions.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('user.sessions.none') }}</p>
            <table v-else class="w-full text-left text-sm" data-testid="user-sessions">
              <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
                <tr>
                  <th scope="col" class="px-4 py-2">{{ t('user.sessions.created') }}</th>
                  <th scope="col" class="px-4 py-2">{{ t('user.sessions.lastUsed') }}</th>
                  <th scope="col" class="px-4 py-2">{{ t('user.sessions.expires') }}</th>
                  <th v-if="canWrite" scope="col" class="px-4 py-2"><span class="sr-only">{{ t('user.sessions.revoke') }}</span></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in sessions" :key="s.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                  <td class="px-4 py-2">{{ formatDate(s.created_at) }}</td>
                  <td class="px-4 py-2">{{ formatDate(s.last_used_at) }}</td>
                  <td class="px-4 py-2">{{ formatDate(s.expires_at) }}</td>
                  <td v-if="canWrite" class="px-4 py-2 text-right"><AppButton variant="secondary" @click="revokeSession(s.id)">{{ t('user.sessions.revoke') }}</AppButton></td>
                </tr>
              </tbody>
            </table>
          </AppCard>
        </section>
        <section v-if="pushConfigured" aria-labelledby="devices-h">
          <AppCard flush>
            <h2 id="devices-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('user.devices.title') }}</h2>
            <AppAlert v-if="devicesError" kind="error" class="m-3">{{ devicesError }}</AppAlert>
            <p v-else-if="!devices.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400" data-testid="user-devices-none">{{ t('user.devices.none') }}</p>
            <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800" data-testid="user-devices">
              <li v-for="d in devices" :key="d.id" class="flex items-center gap-3 p-4 text-[13px]">
                <PhDeviceMobileCamera :size="20" class="text-slate-400" aria-hidden="true" />
                <div class="flex-1"><b>{{ d.name ?? '—' }}</b><div class="text-xs text-slate-600 dark:text-slate-400"><span class="font-mono">{{ d.platform }}</span> · {{ t('user.devices.lastSeen') }} {{ formatDate(d.last_seen) }}</div></div>
                <AppButton v-if="canWrite" variant="danger" @click="removeDevice(d.id)">{{ t('user.devices.remove') }}</AppButton>
              </li>
            </ul>
          </AppCard>
        </section>
      </div>
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

    <ConfirmDialog v-model:open="removePasskeyOpen" :title="t('user.passkeys.removeTitle', { name: removingPasskey?.name ?? '' })" :description="t('user.passkeys.removeBody')" :expected="removingPasskey?.name ?? ''" :action="t('user.passkeys.remove')" @confirm="removePasskey" />
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
