<script setup lang="ts">
import type { Admin } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// Schema migrations: for each collection with a versioned schema, how many documents are still below its
// current version and what the latest migration did. Starting one runs a job (a dry run only counts).
type Listing = Awaited<ReturnType<Admin['dataMigrations']['list']>>
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const canWrite = computed(() => session.canWrite('data'))

const listing = ref<Listing>()
const error = ref('')
const busy = ref('')
async function load() {
  error.value = ''
  try {
    listing.value = await admin.dataMigrations.list()
  } catch (e) {
    error.value = errorText(e, t('migrations.loadFailed'))
  }
}
onMounted(load)

async function start(database: string, collection: string, dryRun: boolean) {
  busy.value = `${database}/${collection}`
  try {
    const r = await admin.dataMigrations.start({ database, collection, dryRun })
    toasts.push(t(r.already_running ? 'migrations.already' : dryRun ? 'migrations.dryStarted' : 'migrations.started', { collection: `${database}/${collection}` }))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    busy.value = ''
  }
}
const running = (database: string, collection: string) => (listing.value?.running ?? []).some((j) => JSON.stringify(j).includes(`"${database}"`) && JSON.stringify(j).includes(`"${collection}"`))
</script>

<template>
  <p class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ t('migrations.help') }}</p>
  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <AppCard v-else-if="listing" flush>
    <table class="w-full text-left text-[13px]" data-testid="migrations-table">
      <thead class="text-xs text-slate-500"><tr><th class="px-3 py-2 font-medium">{{ t('checks.collection') }}</th><th class="px-3 py-2 font-medium">{{ t('migrations.version') }}</th><th class="px-3 py-2 font-medium">{{ t('migrations.below') }}</th><th class="px-3 py-2 font-medium">{{ t('migrations.last') }}</th><th class="px-3 py-2" /></tr></thead>
      <tbody>
        <tr v-for="it in listing.items" :key="it.database + it.collection" class="border-t border-slate-100 align-top dark:border-slate-800">
          <td class="px-3 py-2 font-mono text-xs">{{ it.database }}/{{ it.collection }}</td>
          <td class="px-3 py-2">v{{ it.current_version }}</td>
          <td class="px-3 py-2"><AppBadge v-if="it.documents_below_current === null">—</AppBadge><AppBadge v-else :kind="it.documents_below_current ? 'warn' : 'ok'">{{ it.documents_below_current }}</AppBadge></td>
          <td class="px-3 py-2 text-xs text-slate-500">
            <template v-if="it.report">{{ it.report.dry_run ? t('migrations.dry') : '' }} {{ it.report.ending ?? it.report.status }} · {{ t('migrations.counts', { migrated: it.report.migrated, failed: it.report.failed }) }} · {{ formatDate(it.report.finished_at ?? it.report.created_at) }}</template>
            <template v-else>{{ t('migrations.never') }}</template>
          </td>
          <td class="px-3 py-2 text-right whitespace-nowrap">
            <template v-if="canWrite">
              <AppButton variant="secondary" class="!h-7 !px-2 !text-xs" :busy="busy === it.database + '/' + it.collection || running(it.database, it.collection)" @click="start(it.database, it.collection, true)">{{ t('migrations.dryRun') }}</AppButton>
              <AppButton class="ml-1 !h-7 !px-2 !text-xs" :busy="busy === it.database + '/' + it.collection || running(it.database, it.collection)" @click="start(it.database, it.collection, false)">{{ t('migrations.migrate') }}</AppButton>
            </template>
          </td>
        </tr>
        <tr v-if="!listing.items.length"><td colspan="5" class="px-3 py-5 text-center text-slate-500">{{ t('migrations.none') }}</td></tr>
      </tbody>
    </table>
  </AppCard>
</template>
