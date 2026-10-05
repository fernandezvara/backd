<script setup lang="ts">
import type { AuditRecord } from 'backd-js'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()

const PAGE = 50
const filters = reactive({ action: '', actor: '', target: '', since: '', until: '' })
const records = ref<AuditRecord[]>([])
const skip = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')

// datetime-local gives local time without a zone; the server wants an instant.
const instant = (v: string) => (v ? new Date(v).toISOString() : undefined)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await admin.audit.list({
      action: filters.action.trim() || undefined,
      actor: filters.actor.trim() || undefined,
      target: filters.target.trim() || undefined,
      since: instant(filters.since),
      until: instant(filters.until),
      limit: PAGE,
      skip: skip.value,
    })
    records.value = page.items
    hasMore.value = page.has_more
  } catch (e) {
    error.value = errorText(e, t('audit.loadFailed'))
  } finally {
    loading.value = false
  }
}
function apply() {
  skip.value = 0
  void load()
}
function clear() {
  Object.assign(filters, { action: '', actor: '', target: '', since: '', until: '' })
  apply()
}
function page(delta: number) {
  skip.value = Math.max(0, skip.value + delta * PAGE)
  void load()
}
onMounted(load)

const range = computed(() => ({ from: records.value.length ? skip.value + 1 : 0, to: skip.value + records.value.length }))

// "user:<id>" links to the user's page for a level that may read users.
const userId = (ref: string | null) => (ref && ref.startsWith('user:') && session.canRead('users') ? ref.slice(5) : null)
// Details are always text: pretty JSON in a <pre>, never markup.
const detailsText = (r: AuditRecord) => (Object.keys(r.details ?? {}).length ? JSON.stringify(r.details, null, 2) : '')
</script>

<template>
  <h1 class="text-2xl font-semibold">{{ t('audit.title') }}</h1>
  <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('audit.help') }}</p>

  <form class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5" role="search" @submit.prevent="apply">
    <TextField v-model="filters.action" :label="t('audit.action')" autocomplete="off" />
    <TextField v-model="filters.actor" :label="t('audit.actor')" autocomplete="off" />
    <TextField v-model="filters.target" :label="t('audit.target')" autocomplete="off" />
    <TextField v-model="filters.since" type="datetime-local" :label="t('audit.since')" />
    <TextField v-model="filters.until" type="datetime-local" :label="t('audit.until')" />
    <div class="flex gap-2 sm:col-span-2 lg:col-span-5">
      <AppButton type="submit" :busy="loading">{{ t('audit.filter') }}</AppButton>
      <AppButton variant="secondary" @click="clear">{{ t('common.reset') }}</AppButton>
    </div>
  </form>

  <AppAlert v-if="error" kind="error" class="mt-4">{{ error }}</AppAlert>

  <div class="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <table class="w-full text-left text-sm" data-testid="audit-table">
      <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
        <tr>
          <th scope="col" class="px-3 py-2">{{ t('audit.time') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('audit.action') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('audit.actor') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('audit.target') }}</th>
          <th scope="col" class="px-3 py-2">{{ t('audit.details') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in records" :key="r.id" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
          <td class="px-3 py-2 whitespace-nowrap">{{ formatDate(r.at) }}</td>
          <td class="px-3 py-2 font-mono text-xs">{{ r.action }}</td>
          <td class="px-3 py-2 font-mono text-xs break-all">
            <RouterLink v-if="userId(r.actor)" :to="{ name: 'user', params: { realm, id: userId(r.actor)! } }" class="underline">{{ r.actor }}</RouterLink>
            <template v-else>{{ r.actor }}</template>
          </td>
          <td class="px-3 py-2 font-mono text-xs break-all">
            <RouterLink v-if="userId(r.target)" :to="{ name: 'user', params: { realm, id: userId(r.target)! } }" class="underline">{{ r.target }}</RouterLink>
            <template v-else>{{ r.target ?? '—' }}</template>
          </td>
          <td class="px-3 py-2">
            <details v-if="detailsText(r) || r.request_id || r.client_ip">
              <summary class="cursor-pointer text-xs underline">{{ t('common.details') }}</summary>
              <dl class="mt-1 space-y-1 text-xs">
                <div v-if="r.request_id"><dt class="inline text-slate-600 dark:text-slate-400">{{ t('audit.request') }}: </dt><dd class="inline font-mono">{{ r.request_id }}</dd></div>
                <div v-if="r.client_ip"><dt class="inline text-slate-600 dark:text-slate-400">{{ t('audit.ip') }}: </dt><dd class="inline font-mono">{{ r.client_ip }}</dd></div>
              </dl>
              <pre v-if="detailsText(r)" class="mt-1 max-w-xs overflow-x-auto rounded bg-slate-100 p-2 text-xs dark:bg-slate-800" tabindex="0">{{ detailsText(r) }}</pre>
            </details>
            <span v-else class="text-xs text-slate-600 dark:text-slate-400">{{ t('audit.noDetails') }}</span>
          </td>
        </tr>
        <tr v-if="!records.length && !loading">
          <td colspan="5" class="px-3 py-6 text-center text-slate-600 dark:text-slate-400">{{ t('audit.empty') }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <div class="mt-3 flex items-center gap-3">
    <AppButton variant="secondary" :disabled="skip === 0 || loading" @click="page(-1)">{{ t('common.previous') }}</AppButton>
    <AppButton variant="secondary" :disabled="!hasMore || loading" @click="page(1)">{{ t('common.next') }}</AppButton>
    <span v-if="records.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('audit.page', { from: range.from, to: range.to }) }}</span>
  </div>
</template>
