<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import TextField from '@/components/TextField.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// What a user's data export takes from each collection, and asking users to review a new version of a
// consent. Exports themselves are made from the user's own page (Privacy).
defineProps<{ realm: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()
const toasts = useToasts()

interface Scope {
  realm: string
  auth: boolean
  collections: { database: string; collection: string; export: 'include' | 'exclude'; owned: boolean; fields?: string[]; pull: string[]; files: string[]; reason?: string; declared: boolean; organization?: boolean }[]
  system: { included: string[]; excluded: string[] }
}
const scope = ref<Scope>()
const error = ref('')
onMounted(async () => {
  if (!session.canRead('config')) return
  try {
    scope.value = await raw<Scope>('GET', ['compliance', 'scope'])
  } catch (e) {
    error.value = errorText(e, t('compliance.scope.loadFailed'))
  }
})

// Ask users to review a consent
const asking = ref(false)
const consent = ref('')
const busy = ref(false)
const askError = ref('')
async function ask() {
  askError.value = ''
  if (!consent.value.trim()) {
    askError.value = t('compliance.notify.required')
    return
  }
  busy.value = true
  try {
    const r = await raw<{ consent: string; version: string; matched: number; queued: number; limited: number }>('POST', ['consents', 'notify'], { body: { consent: consent.value.trim() } })
    toasts.push(t('compliance.notify.done', { consent: r.consent, version: r.version, matched: r.matched, queued: r.queued, limited: r.limited }))
    asking.value = false
  } catch (e) {
    askError.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader :title="t('compliance.title')" :description="t('compliance.description')">
    <AppButton v-if="session.canWrite('users')" @click="((asking = true), (consent = ''), (askError = ''))">{{ t('compliance.notify.open') }}</AppButton>
  </PageHeader>

  <h2 class="mb-2 text-sm font-semibold">{{ t('compliance.scope.title') }}</h2>
  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <AppAlert v-else-if="!session.canRead('config')">{{ t('compliance.scope.needsConfig') }}</AppAlert>
  <template v-else-if="scope">
    <AppAlert v-if="!scope.auth">{{ t('compliance.scope.noAuth') }}</AppAlert>
    <AppCard v-else flush data-testid="scope-table">
      <table class="w-full text-left text-[13px]">
        <thead class="text-xs text-slate-500">
          <tr>
            <th class="px-3 py-2 font-medium">{{ t('compliance.scope.collection') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('compliance.scope.export') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('compliance.scope.what') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('compliance.scope.reason') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in scope.collections" :key="c.database + c.collection" class="border-t border-slate-100 align-top dark:border-slate-800">
            <td class="px-3 py-2"><span class="font-mono">{{ c.database }}/{{ c.collection }}</span></td>
            <td class="px-3 py-2"><AppBadge :kind="c.export === 'include' ? 'ok' : 'neutral'">{{ t(`compliance.scope.${c.export}`) }}</AppBadge></td>
            <td class="px-3 py-2 text-slate-600 dark:text-slate-400">
              <template v-if="c.export === 'include'">
                <span v-if="c.owned">{{ t('compliance.scope.owned') }}</span>
                <span v-if="c.fields?.length"> · {{ t('compliance.scope.fields', { fields: c.fields.join(', ') }) }}</span>
                <span v-if="c.pull.length"> · {{ t('compliance.scope.pull', { fields: c.pull.join(', ') }) }}</span>
                <span v-if="c.files.length"> · {{ t('compliance.scope.files', { fields: c.files.join(', ') }) }}</span>
                <span v-if="c.organization"> · {{ t('compliance.scope.organization') }}</span>
              </template>
            </td>
            <td class="px-3 py-2 text-slate-600 dark:text-slate-400">
              {{ c.reason ?? '' }}
              <AppBadge v-if="!c.declared" kind="warn" class="ml-1">{{ t('compliance.scope.undeclared') }}</AppBadge>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="grid gap-3 border-t border-slate-200 p-3 text-[13px] sm:grid-cols-2 dark:border-slate-800">
        <p><b>{{ t('compliance.scope.systemIn') }}</b> {{ scope.system.included.join(', ') || '—' }}</p>
        <p><b>{{ t('compliance.scope.systemOut') }}</b> {{ scope.system.excluded.join(', ') || '—' }}</p>
      </div>
    </AppCard>
  </template>

  <FormDialog v-model:open="asking" :title="t('compliance.notify.title')" :description="t('compliance.notify.help')" :submit="t('compliance.notify.submit')" :busy="busy" :error="askError" @submit="ask">
    <TextField v-model="consent" :label="t('compliance.notify.name')" autocomplete="off" />
  </FormDialog>
</template>
