<script setup lang="ts">
import AppCard from '@/components/AppCard.vue'
import type { StorageStatus } from 'backd-js'
import { PhArrowsClockwise, PhGlobe, PhHardDrives, PhUsers } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import PageHeader from '@/components/PageHeader.vue'
import StorageToolsPanel from '@/components/StorageToolsPanel.vue'
import { useAdmin } from '@/lib/admin'
import { formatBytes } from 'backd-js'
import { errorText, formatDate, percentOf } from '@/lib/format'
import { useSession } from '@/stores/session'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()

const status = ref<StorageStatus>()
const loading = ref(false)
const error = ref('')
const loadedAt = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    status.value = await admin.storage.status()
    loadedAt.value = new Date().toISOString()
  } catch (e) {
    error.value = errorText(e, t('storage.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

const usage = computed(() => status.value?.usage)
const realmQuota = computed(() => status.value?.quota?.realm ?? null)
const userQuota = computed(() => status.value?.quota?.user ?? null)
const realmPct = computed(() => (usage.value && realmQuota.value ? percentOf(usage.value.bytes, realmQuota.value) : 0))
const userPct = (bytes: number) => (userQuota.value ? percentOf(bytes, userQuota.value) : 0)
// Near a limit (or past it) is worth saying before an upload is refused.
const nearLimit = computed(() => realmPct.value >= 90 || (status.value?.usage?.users ?? []).some((u) => userPct(u.bytes) >= 90))
const tone = (pct: number) => (pct >= 100 ? 'bg-red-600' : pct >= 80 ? 'bg-amber-500' : 'bg-brand-700')
const canOpenUsers = computed(() => session.canRead('users'))
</script>

<template>
  <PageHeader :title="t('storage.title')" :description="t('nav.desc.storage')">
    <span v-if="loadedAt" class="text-xs text-slate-600 dark:text-slate-400">{{ t('storage.asOf', { date: formatDate(loadedAt) }) }}</span>
    <AppButton variant="secondary" :busy="loading" @click="load"><PhArrowsClockwise :size="15" aria-hidden="true" />{{ t('storage.refresh') }}</AppButton>
  </PageHeader>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <template v-else-if="status">
    <AppAlert v-if="!status.configured && !status.public_storage" class="mt-4" data-testid="storage-none">{{ t('storage.none') }}</AppAlert>

    <div class="grid gap-3 lg:grid-cols-2">
      <AppCard v-if="status.configured" data-testid="storage-where">
        <div class="flex flex-wrap items-center gap-3">
          <PhHardDrives :size="22" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />
          <div class="min-w-0 flex-1">
            <div class="text-[13px] font-semibold">{{ t('storage.privateTitle') }}</div>
            <code class="block truncate font-mono text-xs text-slate-600 dark:text-slate-400">{{ status.provider }} · {{ status.bucket }}/{{ status.prefix }}</code>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <AppBadge :kind="status.keys?.ok ? 'ok' : 'error'">{{ t('storage.keys') }}: {{ status.keys?.ok ? t('storage.ok') : t('storage.notUsable') }}</AppBadge>
            <AppBadge :kind="status.reachable?.ok ? 'ok' : 'error'">{{ t('storage.bucket') }}: {{ status.reachable?.ok ? t('storage.reachable') : t('storage.unreachable') }}</AppBadge>
          </div>
        </div>
        <AppAlert v-if="status.keys && !status.keys.ok" kind="error" class="mt-3">{{ status.keys.error }}</AppAlert>
        <AppAlert v-else-if="status.reachable && !status.reachable.ok" kind="error" class="mt-3">{{ status.reachable.error }}</AppAlert>
      </AppCard>

      <AppCard v-if="status.public_storage" data-testid="storage-public">
        <div class="flex flex-wrap items-center gap-3">
          <PhGlobe :size="22" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />
          <div class="min-w-0 flex-1">
            <div class="text-[13px] font-semibold">{{ t('storage.publicTitle') }}</div>
            <code class="block truncate font-mono text-xs text-slate-600 dark:text-slate-400" data-testid="storage-public-where">{{ status.public_storage.provider }} · {{ status.public_storage.bucket }}/{{ status.public_storage.prefix }}</code>
            <div class="mt-0.5 text-xs text-slate-500">{{ t('storage.publicWhere', { url: status.public_storage.url }) }}</div>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <AppBadge :kind="status.public_storage.keys.ok ? 'ok' : 'error'">{{ t('storage.keys') }}: {{ status.public_storage.keys.ok ? t('storage.ok') : t('storage.notUsable') }}</AppBadge>
            <AppBadge :kind="status.public_storage.reachable.ok ? 'ok' : 'error'">{{ t('storage.bucket') }}: {{ status.public_storage.reachable.ok ? t('storage.reachable') : t('storage.unreachable') }}</AppBadge>
          </div>
        </div>
        <AppAlert v-if="!status.public_storage.keys.ok" kind="error" class="mt-3">{{ status.public_storage.keys.error }}</AppAlert>
        <AppAlert v-else-if="!status.public_storage.reachable.ok" kind="error" class="mt-3">{{ status.public_storage.reachable.error }}</AppAlert>
      </AppCard>
    </div>

    <template v-if="status.configured">
      <AppAlert v-if="nearLimit" kind="error" class="mt-4" data-testid="storage-near">{{ t('storage.near') }}</AppAlert>

      <div class="mt-4 grid gap-3 sm:grid-cols-3">
        <AppCard>
          <div class="text-xs font-medium text-slate-500">{{ t('storage.usage') }}</div>
          <div class="mt-1 text-2xl font-semibold" data-testid="storage-usage">{{ formatBytes(usage?.bytes ?? 0) }}<span v-if="realmQuota" class="ml-1 text-sm font-normal text-slate-500">{{ t('storage.ofLimit', { limit: formatBytes(realmQuota) }) }}</span></div>
          <div v-if="realmQuota" class="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800" role="meter" :aria-label="t('storage.realmMeter')" aria-valuemin="0" :aria-valuemax="realmQuota" :aria-valuenow="Math.min(usage?.bytes ?? 0, realmQuota)" data-testid="realm-meter">
            <div class="h-full rounded-full" :class="tone(realmPct)" :style="{ width: Math.min(realmPct, 100) + '%' }"></div>
          </div>
          <p v-else class="mt-1 text-xs text-slate-500">{{ t('storage.noRealmQuota') }}</p>
          <p class="mt-2 text-xs text-slate-500" data-testid="user-quota">{{ userQuota ? t('storage.userQuota', { limit: formatBytes(userQuota) }) : t('storage.noUserQuota') }}</p>
        </AppCard>
        <AppCard>
          <div class="text-xs font-medium text-slate-500">{{ t('storage.files') }}</div>
          <div class="mt-1 text-2xl font-semibold" data-testid="storage-files">{{ (usage?.files ?? 0).toLocaleString() }}</div>
          <p class="mt-2 text-xs text-slate-500">{{ t('storage.referenced') }}</p>
        </AppCard>
        <AppCard>
          <div class="text-xs font-medium text-slate-500">{{ t('storage.cleanup') }}</div>
          <div class="mt-1 text-2xl font-semibold" data-testid="storage-queue">{{ status.deletions?.queued ?? 0 }}<span class="ml-1 text-sm font-normal text-slate-500">{{ t('storage.waitingDelete') }}</span></div>
          <p class="mt-2 text-xs text-slate-500">
            {{ t('storage.retrying', { n: status.deletions?.retrying ?? 0 }) }} · {{ t('storage.staleShort', { n: status.stale_uploads ?? 0 }) }}
            <template v-if="status.deletions?.oldest"><br />{{ t('storage.oldest', { date: formatDate(status.deletions.oldest) }) }}</template>
          </p>
        </AppCard>
      </div>

      <section class="mt-5" aria-labelledby="users-h">
        <h2 id="users-h" class="mb-2 flex items-center gap-2 text-[13px] font-semibold"><PhUsers :size="15" class="text-slate-400" aria-hidden="true" />{{ t('storage.users') }}</h2>
        <p v-if="!usage?.users.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('storage.noUsers') }}</p>
        <AppCard v-else flush>
          <table class="w-full text-left text-sm" data-testid="storage-users">
            <thead>
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('storage.user') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('storage.files') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('storage.size') }}</th>
                <th v-if="userQuota" scope="col" class="px-3 py-2">{{ t('storage.ofUserQuota') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in usage.users" :key="u.user_id" class="border-t border-slate-100 dark:border-slate-800">
                <th scope="row" class="px-3 py-1.5 font-mono text-xs font-normal">
                  <RouterLink v-if="canOpenUsers" :to="{ name: 'user', params: { realm, id: u.user_id } }" class="text-brand-700 hover:underline dark:text-brand-400">{{ u.user_id }}</RouterLink>
                  <template v-else>{{ u.user_id }}</template>
                </th>
                <td class="px-3 py-1.5">{{ u.files }}</td>
                <td class="px-3 py-1.5">{{ formatBytes(u.bytes) }}</td>
                <td v-if="userQuota" class="px-3 py-1.5">
                  <span :class="userPct(u.bytes) >= 100 ? 'font-semibold text-red-700 dark:text-red-400' : ''">{{ userPct(u.bytes) }}%</span>
                  <div class="mt-1 h-1.5 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800" role="meter" :aria-label="t('storage.userMeter', { id: u.user_id })" aria-valuemin="0" :aria-valuemax="userQuota" :aria-valuenow="Math.min(u.bytes, userQuota)">
                    <div class="h-full rounded-full" :class="tone(userPct(u.bytes))" :style="{ width: Math.min(userPct(u.bytes), 100) + '%' }"></div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </AppCard>
        <p class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t('storage.usersNote') }}</p>
      </section>
    </template>
    <StorageToolsPanel v-if="status.configured || status.public_storage" :public-too="!!status.public_storage" />
  </template>
</template>
