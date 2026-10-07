<script setup lang="ts">
import type { StorageStatus } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatBytes, formatDate, percentOf } from '@/lib/format'
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
  <div class="flex flex-wrap items-baseline justify-between gap-2">
    <h1 class="text-2xl font-semibold">{{ t('storage.title') }}</h1>
    <div class="flex items-center gap-3">
      <span v-if="loadedAt" class="text-xs text-slate-600 dark:text-slate-400">{{ t('storage.asOf', { date: formatDate(loadedAt) }) }}</span>
      <AppButton variant="secondary" :busy="loading" @click="load">{{ t('storage.refresh') }}</AppButton>
    </div>
  </div>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <template v-else-if="status">
    <AppAlert v-if="!status.configured" class="mt-4" data-testid="storage-none">{{ t('storage.none') }}</AppAlert>

    <template v-else>
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-400" data-testid="storage-where">
        <code class="font-mono">{{ status.provider }} · {{ status.bucket }}/{{ status.prefix }}</code>
        — {{ t('storage.keys') }}: <strong>{{ status.keys?.ok ? t('storage.ok') : t('storage.notUsable') }}</strong>,
        {{ t('storage.bucket') }}: <strong>{{ status.reachable?.ok ? t('storage.reachable') : t('storage.unreachable') }}</strong>
      </p>
      <AppAlert v-if="status.keys && !status.keys.ok" kind="error" class="mt-2">{{ status.keys.error }}</AppAlert>
      <AppAlert v-else-if="status.reachable && !status.reachable.ok" kind="error" class="mt-2">{{ status.reachable.error }}</AppAlert>

      <AppAlert v-if="nearLimit" kind="error" class="mt-4" data-testid="storage-near">{{ t('storage.near') }}</AppAlert>

      <section class="mt-6 max-w-3xl" aria-labelledby="usage-h">
        <h2 id="usage-h" class="text-lg font-semibold">{{ t('storage.usage') }}</h2>
        <p class="mt-1" data-testid="storage-usage">
          <strong>{{ formatBytes(usage?.bytes ?? 0) }}</strong> {{ t('storage.inFiles', { n: usage?.files ?? 0 }) }}
          <template v-if="realmQuota"> {{ t('storage.ofQuota', { pct: realmPct, limit: formatBytes(realmQuota) }) }}</template>
        </p>
        <div v-if="realmQuota" class="mt-2 h-3 w-full overflow-hidden rounded bg-slate-200 dark:bg-slate-700" role="meter" :aria-label="t('storage.realmMeter')" aria-valuemin="0" :aria-valuemax="realmQuota" :aria-valuenow="Math.min(usage?.bytes ?? 0, realmQuota)" data-testid="realm-meter">
          <div class="h-full" :class="tone(realmPct)" :style="{ width: Math.min(realmPct, 100) + '%' }"></div>
        </div>
        <p v-else class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('storage.noRealmQuota') }}</p>
        <p class="mt-1 text-sm text-slate-600 dark:text-slate-400" data-testid="user-quota">{{ userQuota ? t('storage.userQuota', { limit: formatBytes(userQuota) }) : t('storage.noUserQuota') }}</p>
      </section>

      <section class="mt-6" aria-labelledby="users-h">
        <h2 id="users-h" class="text-lg font-semibold">{{ t('storage.users') }}</h2>
        <p v-if="!usage?.users.length" class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('storage.noUsers') }}</p>
        <div v-else class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table class="w-full text-left text-sm" data-testid="storage-users">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('storage.user') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('storage.files') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('storage.size') }}</th>
                <th v-if="userQuota" scope="col" class="px-3 py-2">{{ t('storage.ofUserQuota') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in usage.users" :key="u.user_id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <th scope="row" class="px-3 py-1.5 font-mono text-xs font-normal">
                  <RouterLink v-if="canOpenUsers" :to="{ name: 'user', params: { realm, id: u.user_id } }" class="underline">{{ u.user_id }}</RouterLink>
                  <template v-else>{{ u.user_id }}</template>
                </th>
                <td class="px-3 py-1.5">{{ u.files }}</td>
                <td class="px-3 py-1.5">{{ formatBytes(u.bytes) }}</td>
                <td v-if="userQuota" class="px-3 py-1.5">
                  <span :class="userPct(u.bytes) >= 100 ? 'font-semibold text-red-700 dark:text-red-400' : ''">{{ userPct(u.bytes) }}%</span>
                  <div class="mt-1 h-1.5 w-24 overflow-hidden rounded bg-slate-200 dark:bg-slate-700" role="meter" :aria-label="t('storage.userMeter', { id: u.user_id })" aria-valuemin="0" :aria-valuemax="userQuota" :aria-valuenow="Math.min(u.bytes, userQuota)">
                    <div class="h-full" :class="tone(userPct(u.bytes))" :style="{ width: Math.min(userPct(u.bytes), 100) + '%' }"></div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t('storage.usersNote') }}</p>
      </section>

      <section class="mt-6 max-w-3xl" aria-labelledby="queue-h">
        <h2 id="queue-h" class="text-lg font-semibold">{{ t('storage.cleanup') }}</h2>
        <p class="mt-1 text-sm" data-testid="storage-queue">
          {{ t('storage.queued', { n: status.deletions?.queued ?? 0, retrying: status.deletions?.retrying ?? 0 }) }}
          <template v-if="status.deletions?.oldest"> {{ t('storage.oldest', { date: formatDate(status.deletions.oldest) }) }}</template>
        </p>
        <p class="mt-1 text-sm">{{ t('storage.stale', { n: status.stale_uploads ?? 0 }) }}</p>
        <p class="mt-3 text-xs text-slate-600 dark:text-slate-400">{{ t('storage.reconcileNote') }}</p>
      </section>
    </template>
  </template>
</template>
