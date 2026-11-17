<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FunctionsTabs from '@/components/FunctionsTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// Collection triggers: whether the dispatcher is running and how far behind it is, each trigger the
// configuration declares, and the gaps (changes that may have been missed) waiting to be resolved.
defineProps<{ realm: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()
const toasts = useToasts()

interface Status {
  dispatcher: { started: boolean; held: boolean; epoch: number; watermark_at: string | null; watermark_age_seconds: number | null; lease_expires_at: string | null } | null
  triggers: { database: string; collection: string; name: string; events: string[]; when: string | null; function: string; fields: string[] | null; session: boolean; ordered: boolean }[]
  backlog: number | null
  open_gaps: number | null
}
interface Gap { id: string; reason: string; collection: string | null; trigger: string | null; from: string; to: string; count: number; recorded_at: string; resolved_at: string | null }
const status = ref<Status>()
const gaps = ref<Gap[]>([])
const error = ref('')
const canWrite = session.canWrite('functions')

async function load() {
  error.value = ''
  try {
    ;[status.value, gaps.value] = await Promise.all([raw<Status>('GET', ['triggers', 'status']), raw<{ items: Gap[] }>('GET', ['triggers', 'gaps']).then((r) => r.items)])
  } catch (e) {
    error.value = errorText(e, t('triggers.loadFailed'))
  }
}
onMounted(load)

async function resolve(g: Gap) {
  try {
    await raw('POST', ['triggers', 'gaps', g.id, 'resolve'])
    toasts.push(t('triggers.resolved'))
    await load()
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
</script>

<template>
  <PageHeader :title="t('triggers.title')" :description="t('triggers.description')" />
  <FunctionsTabs />
  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <template v-else-if="status">
    <div class="mb-4 grid gap-4 sm:grid-cols-3">
      <AppCard>
        <p class="text-xs text-slate-500">{{ t('triggers.dispatcher') }}</p>
        <p class="mt-1 flex items-center gap-2 text-sm font-medium">
          <AppBadge v-if="status.dispatcher" :kind="status.dispatcher.held ? 'ok' : 'warn'">{{ status.dispatcher.held ? t('triggers.running') : t('triggers.noLease') }}</AppBadge>
          <AppBadge v-else>{{ t('triggers.unknown') }}</AppBadge>
        </p>
        <p v-if="status.dispatcher?.watermark_at" class="mt-1 text-xs text-slate-500">{{ t('triggers.position', { date: formatDate(status.dispatcher.watermark_at) }) }}</p>
      </AppCard>
      <AppCard><p class="text-xs text-slate-500">{{ t('triggers.backlog') }}</p><p class="mt-1 text-2xl font-semibold" data-testid="backlog">{{ status.backlog ?? '—' }}</p></AppCard>
      <AppCard><p class="text-xs text-slate-500">{{ t('triggers.gaps') }}</p><p class="mt-1 text-2xl font-semibold" data-testid="open-gaps">{{ status.open_gaps ?? '—' }}</p></AppCard>
    </div>

    <h2 class="mb-2 text-sm font-semibold">{{ t('triggers.declared') }}</h2>
    <AppCard flush class="mb-6">
      <table class="w-full text-left text-[13px]" data-testid="triggers-table">
        <thead class="text-xs text-slate-500"><tr><th class="px-3 py-2 font-medium">{{ t('triggers.trigger') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.events') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.when') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.runs') }}</th></tr></thead>
        <tbody>
          <tr v-for="x in status.triggers" :key="x.database + x.collection + x.name" class="border-t border-slate-100 dark:border-slate-800">
            <td class="px-3 py-2"><span class="font-mono">{{ x.database }}/{{ x.collection }}</span> · {{ x.name }}</td>
            <td class="px-3 py-2"><AppBadge v-for="e in x.events" :key="e" class="mr-1">{{ e }}</AppBadge></td>
            <td class="px-3 py-2 font-mono text-xs text-slate-500">{{ x.when ?? t('triggers.always') }}</td>
            <td class="px-3 py-2 font-mono text-xs">{{ x.function }}</td>
          </tr>
          <tr v-if="!status.triggers.length"><td colspan="4" class="px-3 py-5 text-center text-slate-500">{{ t('triggers.none') }}</td></tr>
        </tbody>
      </table>
    </AppCard>

    <h2 class="mb-2 text-sm font-semibold">{{ t('triggers.gapsTitle') }}</h2>
    <AppCard flush>
      <table class="w-full text-left text-[13px]" data-testid="gaps-table">
        <thead class="text-xs text-slate-500"><tr><th class="px-3 py-2 font-medium">{{ t('triggers.reason') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.where') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.window') }}</th><th class="px-3 py-2 font-medium">{{ t('triggers.count') }}</th><th class="px-3 py-2" /></tr></thead>
        <tbody>
          <tr v-for="g in gaps" :key="g.id" class="border-t border-slate-100 dark:border-slate-800">
            <td class="px-3 py-2"><AppBadge kind="warn">{{ g.reason }}</AppBadge></td>
            <td class="px-3 py-2 font-mono text-xs">{{ g.collection ?? t('triggers.allRealms') }}<template v-if="g.trigger"> · {{ g.trigger }}</template></td>
            <td class="px-3 py-2 text-xs text-slate-500">{{ formatDate(g.from) }} → {{ formatDate(g.to) }}</td>
            <td class="px-3 py-2">{{ g.count }}</td>
            <td class="px-3 py-2 text-right"><AppButton v-if="canWrite && !g.resolved_at" variant="secondary" class="!h-7 !px-2 !text-xs" @click="resolve(g)">{{ t('triggers.resolve') }}</AppButton></td>
          </tr>
          <tr v-if="!gaps.length"><td colspan="5" class="px-3 py-5 text-center text-slate-500">{{ t('triggers.noGaps') }}</td></tr>
        </tbody>
      </table>
    </AppCard>
  </template>
</template>
