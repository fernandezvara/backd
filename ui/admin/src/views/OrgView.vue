<script setup lang="ts">
import type { AdminOrg, OrgInvitation, OrgMember, OrgOwnedReport, OrgTeam, OrgTeamRoleGrant } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import JsonField from '@/components/JsonField.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate, parseList } from '@/lib/format'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string; id: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const realmConfig = useRealmConfig()

const canWrite = computed(() => session.canWrite('users'))
const org = ref<AdminOrg>()
const members = ref<OrgMember[]>([])
const teams = ref<OrgTeam[]>([])
const invitations = ref<OrgInvitation[]>([])
const loadError = ref('')

const deleted = computed(() => org.value?.status === 'deleted')
const teamName = (id: string) => teams.value.find((x) => x.id === id)?.name ?? id

// The roles realm.yaml declares for organizations: null when this level can't read the
// configuration, and then no role is flagged.
const declared = computed<string[] | null>(() => {
  const roles = realmConfig.config?.settings.organizations?.roles as Record<string, unknown> | undefined
  return roles ? Object.keys(roles) : null
})
const stale = (role: string) => declared.value !== null && !declared.value.includes(role)
// Members holding a role that manages something (null: the configuration isn't readable).
const managers = computed<number | null>(() => {
  const roles = realmConfig.config?.settings.organizations?.roles as Record<string, { manage?: string[] }> | undefined
  if (!roles) return null
  return members.value.filter((m) => m.effective_roles.some((e) => (roles[e.role]?.manage ?? []).length > 0)).length
})

async function load() {
  loadError.value = ''
  try {
    org.value = await admin.orgs.get(props.id)
    claims.value = JSON.parse(JSON.stringify(org.value.claims))
    if (org.value.status === 'deleted') {
      members.value = []
      teams.value = []
      invitations.value = []
      return
    }
    const [page, ts, invs] = await Promise.all([admin.orgs.members(props.id, { limit: 100 }), admin.orgs.teams(props.id), admin.orgs.invitations(props.id)])
    members.value = page.items
    teams.value = ts
    invitations.value = invs
  } catch (e) {
    loadError.value = errorText(e, t('orgs.loadOneFailed'))
  }
}
onMounted(() => {
  void load()
  void realmConfig.load()
})

async function act<T>(fn: () => Promise<T>, done: string): Promise<boolean> {
  try {
    await fn()
    toasts.push(done)
    await load()
    return true
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
    return false
  }
}

// ---- Members ----
const adding = ref(false)
const addEmail = ref('')
const addRoles = ref('')
const addError = ref('')
const addBusy = ref(false)
function openAdd() {
  addEmail.value = addRoles.value = addError.value = ''
  adding.value = true
}
async function addMember() {
  addBusy.value = true
  addError.value = ''
  try {
    const user = await admin.users.find(addEmail.value.trim())
    if (!user) {
      addError.value = t('orgs.noSuchUser', { email: addEmail.value.trim() })
      return
    }
    await admin.orgs.addMember(props.id, user.id, parseList(addRoles.value))
    adding.value = false
    toasts.push(t('orgs.members.added'))
    await load()
  } catch (e) {
    addError.value = errorText(e, t('common.unexpected'))
  } finally {
    addBusy.value = false
  }
}

const editing = ref(false)
const editTarget = ref<OrgMember>()
const editRoles = ref('')
const editError = ref('')
const editBusy = ref(false)
function openEdit(m: OrgMember) {
  editTarget.value = m
  editRoles.value = m.roles.join(', ')
  editError.value = ''
  editing.value = true
}
async function saveRoles() {
  editBusy.value = true
  editError.value = ''
  try {
    await admin.orgs.setMemberRoles(props.id, editTarget.value!.user, parseList(editRoles.value))
    editing.value = false
    toasts.push(t('orgs.members.updated'))
    await load()
  } catch (e) {
    editError.value = errorText(e, t('common.unexpected'))
  } finally {
    editBusy.value = false
  }
}

const removing = ref(false)
const removeTarget = ref<OrgMember>()
function askRemove(m: OrgMember) {
  removeTarget.value = m
  removing.value = true
}
const memberLabel = (m: OrgMember) => m.email ?? m.user

// ---- Team roles: held within one team, with a window; granted by realm administrators ----
const teamRoleNames = computed<string[]>(() => {
  const roles = realmConfig.config?.settings.organizations?.team_roles as Record<string, unknown> | undefined
  return roles ? Object.keys(roles).sort() : []
})
const teamRoleNeedsMFA = (role: string) => {
  const roles = realmConfig.config?.settings.organizations?.team_roles as Record<string, { require_mfa?: boolean }> | undefined
  return roles?.[role]?.require_mfa === true
}
type Holder = { member: OrgMember; grant: OrgTeamRoleGrant }
// Who holds a team role in a team, in force or not, by team id.
const holdersOf = computed(() => {
  const by = new Map<string, Holder[]>()
  for (const m of members.value) {
    for (const g of m.team_roles ?? []) {
      if (!by.has(g.team)) by.set(g.team, [])
      by.get(g.team)!.push({ member: m, grant: g })
    }
  }
  return by
})
// A grant is in force, still to open or over, as the server judged it when it answered.
function grantState(g: OrgTeamRoleGrant): 'active' | 'upcoming' | 'expired' {
  if (g.active) return 'active'
  return g.from && Date.parse(g.from) > Date.now() ? 'upcoming' : 'expired'
}
// Holders of roles that need a second factor who haven't enrolled one.
const unenrolled = computed(() => members.value.filter((m) => m.mfa === false && (m.mfa_roles ?? []).length > 0))

const granting = ref(false)
const grantTeam = ref<OrgTeam>()
const grantForm = ref({ email: '', role: '', from: '', until: '' })
const grantError = ref('')
const grantBusy = ref(false)
function openGrant(tm: OrgTeam) {
  grantTeam.value = tm
  grantForm.value = { email: '', role: teamRoleNames.value[0] ?? '', from: '', until: '' }
  grantError.value = ''
  granting.value = true
}
// <input type="datetime-local"> holds local time without a zone: the grant is in UTC.
const toISO = (v: string) => (v ? new Date(v).toISOString() : undefined)
async function grantRole() {
  grantBusy.value = true
  grantError.value = ''
  try {
    const f = grantForm.value
    const user = await admin.users.find(f.email.trim())
    if (!user) {
      grantError.value = t('orgs.noSuchUser', { email: f.email.trim() })
      return
    }
    await admin.orgs.grantTeamRole(props.id, grantTeam.value!.id, { user: user.id, role: f.role, from: toISO(f.from), until: toISO(f.until) })
    granting.value = false
    toasts.push(t('orgs.teamRoles.granted'))
    await load()
  } catch (e) {
    grantError.value = errorText(e, t('common.unexpected'))
  } finally {
    grantBusy.value = false
  }
}
const revokingRole = ref(false)
const revokeHolder = ref<Holder & { team: OrgTeam }>()
function askRevokeRole(tm: OrgTeam, h: Holder) {
  revokeHolder.value = { ...h, team: tm }
  revokingRole.value = true
}

// ---- Owner (an orphaned organization) ----
const assigning = ref(false)
const ownerEmail = ref('')
const ownerError = ref('')
const ownerBusy = ref(false)
function openAssign() {
  ownerEmail.value = ownerError.value = ''
  assigning.value = true
}
async function assignOwner() {
  ownerBusy.value = true
  ownerError.value = ''
  try {
    const user = await admin.users.find(ownerEmail.value.trim())
    if (!user) {
      ownerError.value = t('orgs.noSuchUser', { email: ownerEmail.value.trim() })
      return
    }
    await admin.orgs.assignOwner(props.id, user.id)
    assigning.value = false
    toasts.push(t('orgs.owner.done'))
    await load()
  } catch (e) {
    ownerError.value = errorText(e, t('common.unexpected'))
  } finally {
    ownerBusy.value = false
  }
}

// ---- Delete, after its report; reactivate ----
const deleting = ref(false)
const report = ref<OrgOwnedReport>()
const reportError = ref('')
async function askDelete() {
  report.value = undefined
  reportError.value = ''
  deleting.value = true
  try {
    report.value = await admin.orgs.owned(props.id)
  } catch (e) {
    reportError.value = errorText(e, t('orgs.delete.reportFailed'))
  }
}
async function remove() {
  if (await act(() => admin.orgs.delete(props.id), t('orgs.delete.done'))) deleting.value = false
}

const reactivating = ref(false)
const reSlug = ref('')
const reError = ref('')
const reBusy = ref(false)
function openReactivate() {
  reSlug.value = reError.value = ''
  reactivating.value = true
}
async function reactivate() {
  reBusy.value = true
  reError.value = ''
  try {
    await admin.orgs.reactivate(props.id, reSlug.value.trim() ? { slug: reSlug.value.trim() } : {})
    reactivating.value = false
    toasts.push(t('orgs.reactivate.done'))
    await load()
  } catch (e) {
    reError.value = errorText(e, t('common.unexpected'))
  } finally {
    reBusy.value = false
  }
}

// ---- Claims: at most 1 KiB ----
const claims = ref<unknown>({})
const claimsSize = computed(() => new TextEncoder().encode(JSON.stringify(claims.value ?? {})).length)
const claimsError = computed(() => {
  if (claims.value === null || typeof claims.value !== 'object' || Array.isArray(claims.value)) return t('orgs.claims.object')
  return claimsSize.value > 1024 ? t('orgs.claims.tooBig', { size: claimsSize.value }) : ''
})
async function saveClaims() {
  if (claimsError.value) return
  await act(() => admin.orgs.setClaims(props.id, (claims.value ?? {}) as Record<string, unknown>), t('orgs.claims.saved'))
}

// ---- Invitations (tokens are shown once, when made) ----
const inviting = ref(false)
const inv = ref({ binding: 'email' as 'email' | 'user' | 'none', email: '', user: '', roles: '', expires: '' })
const invError = ref('')
const invBusy = ref(false)
const link = ref('')
function openInvite() {
  inv.value = { binding: 'email', email: '', user: '', roles: '', expires: '' }
  invError.value = ''
  inviting.value = true
}
async function invite() {
  invBusy.value = true
  invError.value = ''
  try {
    const v = inv.value
    const made = await admin.orgs.invite(props.id, {
      binding: v.binding,
      email: v.binding === 'email' ? v.email.trim() : undefined,
      user: v.binding === 'user' ? v.user.trim() : undefined,
      roles: parseList(v.roles),
      expiresIn: v.expires.trim() || undefined,
    })
    inviting.value = false
    link.value = made.link ?? made.token
    toasts.push(t('orgs.invitations.created'))
    await load()
  } catch (e) {
    invError.value = errorText(e, t('common.unexpected'))
  } finally {
    invBusy.value = false
  }
}
const linkOpen = computed({ get: () => link.value !== '', set: (v) => { if (!v) link.value = '' } })
const revokeTarget = ref<OrgInvitation>()
const revoking = ref(false)
function askRevoke(i: OrgInvitation) {
  revokeTarget.value = i
  revoking.value = true
}
const invWho = (i: OrgInvitation) => i.email ?? i.user ?? t('orgs.invitations.anyone')

const box = 'rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900'
const th = 'px-3 py-2'
</script>

<template>
  <RouterLink :to="{ name: 'orgs', params: { realm } }" class="text-sm underline">{{ t('orgs.back') }}</RouterLink>
  <AppAlert v-if="loadError" kind="error" class="mt-4">{{ loadError }}</AppAlert>

  <template v-if="org">
    <h1 class="mt-2 text-2xl font-semibold" data-testid="org-name">{{ org.name }}</h1>

    <dl class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <div :class="[box, 'p-4']">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('orgs.statusLabel') }}</dt>
        <dd class="mt-1" data-testid="org-status">{{ t(`orgs.status.${org.status}`) }}</dd>
      </div>
      <div :class="[box, 'p-4']">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('orgs.slug') }}</dt>
        <dd class="mt-1 font-mono text-sm">{{ org.slug ?? '—' }}</dd>
      </div>
      <div :class="[box, 'p-4']">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('orgs.requireMfa') }}</dt>
        <dd class="mt-1">{{ org.require_mfa ? t('common.yes') : t('common.no') }}</dd>
      </div>
      <div :class="[box, 'p-4']">
        <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('orgs.created') }}</dt>
        <dd class="mt-1">{{ formatDate(org.created_at) }}</dd>
      </div>
    </dl>

    <AppAlert v-if="org.status === 'orphaned'" kind="error" class="mt-4" data-testid="org-orphaned">{{ t('orgs.orphanedHelp') }}</AppAlert>
    <AppAlert v-if="deleted" kind="error" class="mt-4">{{ t('orgs.deletedHelp') }}</AppAlert>

    <div v-if="canWrite" class="mt-6 flex flex-wrap gap-2" data-testid="org-actions">
      <AppButton v-if="org.status === 'orphaned'" @click="openAssign">{{ t('orgs.owner.open') }}</AppButton>
      <AppButton v-if="deleted" variant="secondary" @click="openReactivate">{{ t('orgs.reactivate.open') }}</AppButton>
      <AppButton v-if="!deleted" variant="danger" @click="askDelete">{{ t('orgs.delete.open') }}</AppButton>
    </div>

    <template v-if="!deleted">
      <section class="mt-8" aria-labelledby="members-h">
        <div class="flex flex-wrap items-end justify-between gap-3">
          <h2 id="members-h" class="text-lg font-semibold">{{ t('orgs.members.title') }}</h2>
          <AppButton v-if="canWrite" variant="secondary" @click="openAdd">{{ t('orgs.members.add') }}</AppButton>
        </div>
        <div :class="[box, 'mt-2 overflow-x-auto']">
          <table class="w-full text-left text-sm" data-testid="org-members">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" :class="th">{{ t('orgs.members.who') }}</th>
                <th scope="col" :class="th">{{ t('orgs.members.direct') }}</th>
                <th scope="col" :class="th">{{ t('orgs.members.teams') }}</th>
                <th scope="col" :class="th">{{ t('orgs.members.effective') }}</th>
                <th v-if="canWrite" scope="col" :class="th"><span class="sr-only">{{ t('orgs.members.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in members" :key="m.user" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="px-3 py-2">{{ memberLabel(m) }}</td>
                <td class="px-3 py-2">
                  <template v-if="m.roles.length">
                    <span v-for="r in m.roles" :key="r" class="mr-1 rounded bg-slate-200 px-2 py-0.5 dark:bg-slate-700">
                      {{ r }}<span v-if="stale(r)" class="ml-1 text-amber-800 dark:text-amber-300" :title="t('orgs.members.staleHelp')">⚠ {{ t('orgs.members.stale') }}</span>
                    </span>
                  </template>
                  <span v-else class="text-slate-600 dark:text-slate-400">{{ t('common.none') }}</span>
                </td>
                <td class="px-3 py-2">{{ m.teams.length ? m.teams.map(teamName).join(', ') : t('common.none') }}</td>
                <td class="px-3 py-2">
                  <ul class="space-y-0.5">
                    <li v-for="e in m.effective_roles" :key="e.role">
                      <span class="font-medium">{{ e.role }}</span>
                      <span class="text-slate-600 dark:text-slate-400"> ← {{ e.origin.join(', ') }}</span>
                      <span v-if="m.overlap.includes(e.role)" class="ml-1 text-amber-800 dark:text-amber-300" :title="t('orgs.members.overlapHelp')">⚠ {{ t('orgs.members.overlap') }}</span>
                    </li>
                  </ul>
                </td>
                <td v-if="canWrite" class="space-x-3 px-3 py-2 text-right">
                  <button type="button" class="underline" @click="openEdit(m)">{{ t('orgs.members.edit') }}</button>
                  <button type="button" class="underline" @click="askRemove(m)">{{ t('common.delete') }}</button>
                </td>
              </tr>
              <tr v-if="!members.length">
                <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('orgs.members.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">
          {{ managers === null ? t('orgs.members.countOnly', { n: members.length }) : t('orgs.members.count', { n: members.length, managers }) }}
        </p>
      </section>

      <section class="mt-8" aria-labelledby="teams-h">
        <h2 id="teams-h" class="text-lg font-semibold">{{ t('orgs.teams.title') }}</h2>
        <ul class="mt-2 space-y-1 text-sm" data-testid="org-teams">
          <li v-for="tm in teams" :key="tm.id" :data-testid="`team-${tm.name}`">
            <span class="font-medium">{{ tm.name }}</span> — {{ tm.roles.length ? tm.roles.join(', ') : t('common.none') }}
            <span v-for="r in tm.roles.filter(stale)" :key="r" class="ml-1 text-amber-800 dark:text-amber-300">⚠ {{ r }}: {{ t('orgs.members.stale') }}</span>
            <div v-if="holdersOf.get(tm.id)?.length || (canWrite && teamRoleNames.length)" class="mt-1 ml-4">
              <table v-if="holdersOf.get(tm.id)?.length" class="w-full text-left text-xs" :aria-label="t('orgs.teamRoles.of', { team: tm.name })">
                <thead class="text-slate-600 dark:text-slate-400">
                  <tr>
                    <th scope="col" :class="th">{{ t('orgs.members.who') }}</th>
                    <th scope="col" :class="th">{{ t('orgs.teamRoles.role') }}</th>
                    <th scope="col" :class="th">{{ t('orgs.teamRoles.window') }}</th>
                    <th scope="col" :class="th">{{ t('orgs.teamRoles.grantedBy') }}</th>
                    <th scope="col" :class="th">{{ t('orgs.statusLabel') }}</th>
                    <th v-if="canWrite" scope="col" :class="th"><span class="sr-only">{{ t('common.revoke') }}</span></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="h in holdersOf.get(tm.id)" :key="h.member.user + h.grant.role" data-testid="team-role-holder">
                    <td class="px-3 py-1">{{ memberLabel(h.member) }}<span v-if="!h.member.teams.includes(tm.id)" class="ml-1 text-slate-600 dark:text-slate-400">({{ t('orgs.teamRoles.notInTeam') }})</span></td>
                    <td class="px-3 py-1">{{ h.grant.role }}<span v-if="teamRoleNeedsMFA(h.grant.role)" class="ml-1" :title="t('orgs.teamRoles.needsMFA')">🔒</span></td>
                    <td class="px-3 py-1">{{ h.grant.from ? formatDate(h.grant.from) : t('orgs.teamRoles.always') }} → {{ h.grant.until ? formatDate(h.grant.until) : t('orgs.teamRoles.never') }}</td>
                    <td class="px-3 py-1">{{ h.grant.granted_by ?? t('orgs.teamRoles.byAdmin') }}</td>
                    <td class="px-3 py-1">{{ t(`orgs.teamRoles.state.${grantState(h.grant)}`) }}</td>
                    <td v-if="canWrite" class="px-3 py-1 text-right">
                      <button type="button" class="underline" @click="askRevokeRole(tm, h)">{{ t('common.revoke') }}</button>
                    </td>
                  </tr>
                </tbody>
              </table>
              <button v-if="canWrite && teamRoleNames.length" type="button" class="mt-1 underline" @click="openGrant(tm)">{{ t('orgs.teamRoles.grant') }}</button>
            </div>
          </li>
          <li v-if="!teams.length" class="text-slate-600 dark:text-slate-400">{{ t('orgs.teams.none') }}</li>
        </ul>
      </section>

      <section v-if="unenrolled.length" class="mt-8" aria-labelledby="mfa-h">
        <h2 id="mfa-h" class="text-lg font-semibold">{{ t('orgs.teamRoles.unenrolledTitle') }}</h2>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('orgs.teamRoles.unenrolledHelp') }}</p>
        <ul class="mt-2 space-y-1 text-sm" data-testid="org-unenrolled">
          <li v-for="m in unenrolled" :key="m.user">{{ memberLabel(m) }} — {{ (m.mfa_roles ?? []).join(', ') }}</li>
        </ul>
      </section>

      <section class="mt-8" aria-labelledby="inv-h">
        <div class="flex flex-wrap items-end justify-between gap-3">
          <h2 id="inv-h" class="text-lg font-semibold">{{ t('orgs.invitations.title') }}</h2>
          <AppButton v-if="canWrite" variant="secondary" @click="openInvite">{{ t('orgs.invitations.create') }}</AppButton>
        </div>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('orgs.invitations.help') }}</p>
        <div :class="[box, 'mt-2 overflow-x-auto']">
          <table class="w-full text-left text-sm" data-testid="org-invitations">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" :class="th">{{ t('orgs.invitations.for') }}</th>
                <th scope="col" :class="th">{{ t('orgs.invitations.binding') }}</th>
                <th scope="col" :class="th">{{ t('orgs.invitations.roles') }}</th>
                <th scope="col" :class="th">{{ t('orgs.statusLabel') }}</th>
                <th scope="col" :class="th">{{ t('orgs.invitations.expires') }}</th>
                <th v-if="canWrite" scope="col" :class="th"><span class="sr-only">{{ t('common.revoke') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="i in invitations" :key="i.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="px-3 py-2">{{ invWho(i) }}</td>
                <td class="px-3 py-2">{{ i.binding }}</td>
                <td class="px-3 py-2">{{ i.roles.join(', ') || t('common.none') }}</td>
                <td class="px-3 py-2">{{ i.status }}</td>
                <td class="px-3 py-2">{{ formatDate(i.expires_at) }}</td>
                <td v-if="canWrite" class="px-3 py-2 text-right">
                  <button v-if="i.status === 'pending'" type="button" class="underline" @click="askRevoke(i)">{{ t('common.revoke') }}</button>
                </td>
              </tr>
              <tr v-if="!invitations.length">
                <td colspan="6" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('orgs.invitations.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="mt-8" aria-labelledby="claims-h">
        <h2 id="claims-h" class="text-lg font-semibold">{{ t('orgs.claims.title') }}</h2>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('orgs.claims.help') }}</p>
        <div class="mt-2 max-w-xl">
          <JsonField id="org-claims" v-model="claims" :label="t('orgs.claims.label')" :readonly="!canWrite" :rows="6" :error="claimsError" />
          <p class="mt-1 text-xs text-slate-600 dark:text-slate-400" data-testid="claims-size">{{ t('orgs.claims.size', { size: claimsSize }) }}</p>
        </div>
        <AppButton v-if="canWrite" class="mt-2" variant="secondary" :disabled="!!claimsError" @click="saveClaims">{{ t('common.save') }}</AppButton>
      </section>
    </template>
  </template>

  <FormDialog v-model:open="adding" :title="t('orgs.members.add')" :submit="t('common.create')" :busy="addBusy" :error="addError" @submit="addMember">
    <TextField v-model="addEmail" type="email" :label="t('orgs.members.email')" autocomplete="off" required />
    <TextField v-model="addRoles" :label="t('orgs.members.rolesLabel')" :help="t('orgs.members.rolesHelp', { roles: (declared ?? []).join(', ') })" autocomplete="off" />
  </FormDialog>

  <FormDialog v-model:open="editing" :title="t('orgs.members.editTitle', { who: editTarget ? memberLabel(editTarget) : '' })" :submit="t('common.save')" :busy="editBusy" :error="editError" @submit="saveRoles">
    <TextField v-model="editRoles" :label="t('orgs.members.rolesLabel')" :help="t('orgs.members.rolesHelp', { roles: (declared ?? []).join(', ') })" autocomplete="off" />
  </FormDialog>

  <ConfirmDialog
    v-model:open="removing"
    :title="t('orgs.members.removeTitle')"
    :description="t('orgs.members.removeHelp')"
    :expected="removeTarget ? memberLabel(removeTarget) : ''"
    :action="t('orgs.members.remove')"
    @confirm="act(() => admin.orgs.removeMember(id, removeTarget!.user), t('orgs.members.removed'))"
  />

  <FormDialog v-model:open="granting" :title="t('orgs.teamRoles.grantTitle', { team: grantTeam?.name ?? '' })" :description="t('orgs.teamRoles.grantHelp')" :submit="t('orgs.teamRoles.grant')" :busy="grantBusy" :error="grantError" @submit="grantRole">
    <TextField v-model="grantForm.email" type="email" :label="t('orgs.members.email')" autocomplete="off" required />
    <div class="space-y-1">
      <label for="grant-role" class="block text-sm font-medium">{{ t('orgs.teamRoles.role') }}</label>
      <select id="grant-role" v-model="grantForm.role" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option v-for="r in teamRoleNames" :key="r" :value="r">{{ r }}</option>
      </select>
    </div>
    <TextField v-model="grantForm.from" type="datetime-local" :label="t('orgs.teamRoles.from')" :help="t('orgs.teamRoles.fromHelp')" autocomplete="off" />
    <TextField v-model="grantForm.until" type="datetime-local" :label="t('orgs.teamRoles.until')" :help="t('orgs.teamRoles.untilHelp')" autocomplete="off" />
  </FormDialog>

  <ConfirmDialog
    v-model:open="revokingRole"
    :title="t('orgs.teamRoles.revokeTitle')"
    :description="t('orgs.teamRoles.revokeHelp')"
    :expected="revokeHolder ? memberLabel(revokeHolder.member) : ''"
    :action="t('common.revoke')"
    @confirm="act(() => admin.orgs.revokeTeamRole(id, revokeHolder!.team.id, { user: revokeHolder!.member.user, role: revokeHolder!.grant.role }), t('orgs.teamRoles.revoked'))"
  />

  <FormDialog v-model:open="assigning" :title="t('orgs.owner.title')" :description="t('orgs.owner.help')" :submit="t('orgs.owner.open')" :busy="ownerBusy" :error="ownerError" @submit="assignOwner">
    <TextField v-model="ownerEmail" type="email" :label="t('orgs.members.email')" autocomplete="off" required />
  </FormDialog>

  <ConfirmDialog v-model:open="deleting" :title="t('orgs.delete.title')" :description="t('orgs.delete.help')" :expected="org?.name ?? ''" :action="t('orgs.delete.action')" @confirm="remove">
    <AppAlert v-if="reportError" kind="error">{{ reportError }}</AppAlert>
    <div v-else-if="report" data-testid="org-owned">
      <p class="text-sm">{{ t('orgs.delete.summary', { del: report.to_delete, keep: report.to_keep }) }}</p>
      <ul class="mt-2 space-y-1 text-sm">
        <li v-for="c in report.collections" :key="c.database + c.collection">
          <span class="font-mono">{{ c.database }}/{{ c.collection }}</span>: {{ c.documents }} — {{ t(`orgs.delete.policy.${c.on_org_delete}`) }}
        </li>
      </ul>
    </div>
    <p v-else class="text-sm" role="status">{{ t('common.loading') }}</p>
  </ConfirmDialog>

  <FormDialog v-model:open="reactivating" :title="t('orgs.reactivate.title')" :description="t('orgs.reactivate.help')" :submit="t('orgs.reactivate.open')" :busy="reBusy" :error="reError" @submit="reactivate">
    <TextField v-model="reSlug" :label="t('orgs.slug')" :help="t('orgs.reactivate.slugHelp')" autocomplete="off" />
  </FormDialog>

  <FormDialog v-model:open="inviting" :title="t('orgs.invitations.create')" :submit="t('common.create')" :busy="invBusy" :error="invError" @submit="invite">
    <div class="space-y-1">
      <label for="inv-binding" class="block text-sm font-medium">{{ t('orgs.invitations.binding') }}</label>
      <select id="inv-binding" v-model="inv.binding" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="email">{{ t('orgs.invitations.bindings.email') }}</option>
        <option value="user">{{ t('orgs.invitations.bindings.user') }}</option>
        <option value="none">{{ t('orgs.invitations.bindings.none') }}</option>
      </select>
    </div>
    <TextField v-if="inv.binding === 'email'" v-model="inv.email" type="email" :label="t('orgs.members.email')" autocomplete="off" required />
    <TextField v-if="inv.binding === 'user'" v-model="inv.user" :label="t('orgs.invitations.userId')" autocomplete="off" required />
    <TextField v-model="inv.roles" :label="t('orgs.members.rolesLabel')" :help="t('orgs.members.rolesHelp', { roles: (declared ?? []).join(', ') })" autocomplete="off" />
    <TextField v-model="inv.expires" :label="t('orgs.invitations.expiresIn')" :help="t('orgs.invitations.expiresHelp')" autocomplete="off" />
  </FormDialog>

  <FormDialog v-model:open="linkOpen" :title="t('orgs.invitations.linkTitle')" :description="t('orgs.invitations.linkBody')" :submit="t('common.close')" @submit="linkOpen = false">
    <TextField :model-value="link" :label="t('orgs.invitations.linkLabel')" autocomplete="off" data-testid="org-invitation-link" />
  </FormDialog>

  <ConfirmDialog
    v-model:open="revoking"
    :title="t('orgs.invitations.revokeTitle')"
    :expected="t('orgs.invitations.revokeExpected')"
    :action="t('common.revoke')"
    @confirm="act(() => admin.orgs.revokeInvitation(id, revokeTarget!.id), t('orgs.invitations.revoked'))"
  />
</template>
