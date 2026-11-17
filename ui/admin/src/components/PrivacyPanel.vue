<script setup lang="ts">
import { PhDownloadSimple } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText, formatDate } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// One user's privacy records: the consents they hold and their history (with the ones an administrator
// recorded for a paper or phone consent), and the exports of their data, made on request.
const props = defineProps<{ realm: string; id: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()
const toasts = useToasts()
const proof = useProof()
const canWrite = computed(() => session.canWrite('users'))
const full = computed(() => session.access?.level === 'full')

interface Record_ { consent: string; version: string; action: 'accepted' | 'withdrawn'; method: string; locale: string; evidence?: string; at: string }
interface Exp { id: string; status: string; created_at: string; expires_at?: string; size?: number; requested_by: string; downloads: number; ticket?: string; silent?: boolean; reason?: string; error?: string }
const consents = ref<Record<string, string>>({})
const history = ref<Record_[]>([])
const exports_ = ref<Exp[]>([])
const error = ref('')
const exportsUnavailable = ref(false)

async function load() {
  error.value = ''
  try {
    const c = await raw<{ consents: Record<string, string>; history: Record_[] }>('GET', ['users', props.id, 'consents'])
    consents.value = c.consents
    history.value = [...c.history].reverse()
  } catch (e) {
    error.value = errorText(e, t('privacy.loadFailed'))
  }
  try {
    exports_.value = (await raw<{ items: Exp[] }>('GET', ['users', props.id, 'exports'])).items
    exportsUnavailable.value = false
  } catch {
    exportsUnavailable.value = true
  }
}
onMounted(load)

// Record a consent
const recording = ref(false)
const rec = ref({ consent: '', action: 'accepted' as 'accepted' | 'withdrawn', version: '', evidence: '' })
const recError = ref('')
const busy = ref(false)
async function record() {
  recError.value = ''
  const r = rec.value
  if (!r.consent.trim() || !r.evidence.trim() || (r.action === 'accepted' && !r.version.trim())) {
    recError.value = t('privacy.record.required')
    return
  }
  busy.value = true
  try {
    await raw('POST', ['users', props.id, 'consents'], { body: { consent: r.consent.trim(), action: r.action, version: r.action === 'accepted' ? r.version.trim() : undefined, evidence: r.evidence.trim() } })
    toasts.push(t('privacy.record.done'))
    recording.value = false
    await load()
  } catch (e) {
    recError.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}

// Export the user's data
const exporting = ref(false)
const ex = ref({ ticket: '', silent: false, reason: '' })
const exError = ref('')
async function startExport() {
  exError.value = ''
  if (ex.value.silent && !ex.value.reason.trim()) {
    exError.value = t('privacy.export.reasonRequired')
    return
  }
  busy.value = true
  try {
    await proof.prove()
    await raw('POST', ['users', props.id, 'export'], { body: { ticket: ex.value.ticket.trim() || undefined, no_notify: ex.value.silent || undefined, reason: ex.value.silent ? ex.value.reason.trim() : undefined }, reauth: true })
    toasts.push(t('privacy.export.started'))
    exporting.value = false
    await load()
  } catch (e) {
    exError.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}
function openExport() {
  ex.value = { ticket: '', silent: false, reason: '' }
  exError.value = ''
  exporting.value = true
  void proof.prepare()
}

// The package is a zip behind the administrator's session: fetch it with the token and save it.
async function download(e: Exp) {
  try {
    const token = await session.client!.auth.token()
    const res = await fetch(`/v1/${encodeURIComponent(props.realm)}/_admin/users/${encodeURIComponent(props.id)}/exports/${encodeURIComponent(e.id)}/download`, { headers: { Authorization: `Bearer ${token}` } })
    if (!res.ok) throw new Error(String(res.status))
    const url = URL.createObjectURL(await res.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = `export-${e.id}.zip`
    a.click()
    URL.revokeObjectURL(url)
    await load()
  } catch {
    toasts.push(t('privacy.export.downloadFailed'), 'error')
  }
}
const kind = (s: string) => (s === 'ready' ? 'ok' : s === 'failed' ? 'error' : 'neutral')
</script>

<template>
  <AppAlert v-if="error" kind="error" class="mb-4">{{ error }}</AppAlert>

  <section aria-labelledby="consents-h" class="mb-6">
    <div class="mb-2 flex items-center gap-2"><h2 id="consents-h" class="text-sm font-semibold">{{ t('privacy.consents') }}</h2><AppButton v-if="canWrite" variant="secondary" class="ml-auto" @click="((recording = true), (recError = ''), (rec = { consent: '', action: 'accepted', version: '', evidence: '' }))">{{ t('privacy.record.open') }}</AppButton></div>
    <AppCard flush>
      <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800" data-testid="user-consents">
        <li v-for="(v, name) in consents" :key="name" class="flex items-center gap-2 px-4 py-2"><b class="font-mono">{{ name }}</b><AppBadge kind="ok">{{ v }}</AppBadge></li>
        <li v-if="!Object.keys(consents).length" class="px-4 py-3 text-slate-500">{{ t('privacy.noConsents') }}</li>
      </ul>
    </AppCard>
    <details v-if="history.length" class="mt-2 text-[13px]">
      <summary class="cursor-pointer text-xs underline">{{ t('privacy.history', { n: history.length }) }}</summary>
      <ul class="mt-2 space-y-1">
        <li v-for="(h, i) in history" :key="i" class="text-slate-600 dark:text-slate-400"><span class="font-mono">{{ h.consent }}</span> {{ h.version }} · {{ t(`privacy.actions.${h.action}`) }} · {{ h.method }} · {{ formatDate(h.at) }}<template v-if="h.evidence"> · {{ h.evidence }}</template></li>
      </ul>
    </details>
  </section>

  <section aria-labelledby="exports-h">
    <div class="mb-2 flex items-center gap-2"><h2 id="exports-h" class="text-sm font-semibold">{{ t('privacy.exports') }}</h2><AppButton v-if="canWrite && full && !exportsUnavailable" class="ml-auto" @click="openExport">{{ t('privacy.export.open') }}</AppButton></div>
    <p v-if="exportsUnavailable" class="text-sm text-slate-500">{{ t('privacy.exportsOff') }}</p>
    <AppCard v-else flush>
      <table class="w-full text-left text-[13px]" data-testid="user-exports">
        <thead class="text-xs text-slate-500"><tr><th class="px-3 py-2 font-medium">{{ t('privacy.export.when') }}</th><th class="px-3 py-2 font-medium">{{ t('privacy.export.status') }}</th><th class="px-3 py-2 font-medium">{{ t('privacy.export.by') }}</th><th class="px-3 py-2" /></tr></thead>
        <tbody>
          <tr v-for="e in exports_" :key="e.id" class="border-t border-slate-100 dark:border-slate-800">
            <td class="px-3 py-2">{{ formatDate(e.created_at) }}<span v-if="e.ticket" class="ml-1 text-xs text-slate-500">· {{ e.ticket }}</span></td>
            <td class="px-3 py-2"><AppBadge :kind="kind(e.status)">{{ e.status }}</AppBadge><AppBadge v-if="e.silent" kind="warn" class="ml-1">{{ t('privacy.export.silent') }}</AppBadge><span v-if="e.error" class="ml-1 text-xs text-red-700">{{ e.error }}</span></td>
            <td class="px-3 py-2 text-xs text-slate-500">{{ e.requested_by }}</td>
            <td class="px-3 py-2 text-right"><AppButton v-if="e.status === 'ready' && full" variant="secondary" class="!h-7 !px-2 !text-xs" @click="download(e)"><PhDownloadSimple :size="14" aria-hidden="true" />{{ t('privacy.export.download') }}</AppButton></td>
          </tr>
          <tr v-if="!exports_.length"><td colspan="4" class="px-3 py-4 text-center text-slate-500">{{ t('privacy.export.none') }}</td></tr>
        </tbody>
      </table>
    </AppCard>
  </section>

  <FormDialog v-model:open="recording" :title="t('privacy.record.title')" :description="t('privacy.record.help')" :submit="t('common.save')" :busy="busy" :error="recError" @submit="record">
    <TextField v-model="rec.consent" :label="t('privacy.record.consent')" autocomplete="off" required />
    <label class="block space-y-1 text-sm font-medium">{{ t('privacy.record.action') }}
      <select v-model="rec.action" class="block h-8 w-full rounded-md border border-slate-300 bg-white px-2 text-[13px] font-normal dark:border-slate-700 dark:bg-slate-900"><option value="accepted">{{ t('privacy.actions.accepted') }}</option><option value="withdrawn">{{ t('privacy.actions.withdrawn') }}</option></select>
    </label>
    <TextField v-if="rec.action === 'accepted'" v-model="rec.version" :label="t('privacy.record.version')" autocomplete="off" />
    <TextField v-model="rec.evidence" :label="t('privacy.record.evidence')" :help="t('privacy.record.evidenceHelp')" autocomplete="off" required />
  </FormDialog>

  <FormDialog v-model:open="exporting" :title="t('privacy.export.title')" :description="t('privacy.export.help')" :submit="t('privacy.export.submit')" :busy="busy" :error="exError" @submit="startExport">
    <TextField v-model="ex.ticket" :label="t('privacy.export.ticket')" autocomplete="off" />
    <label class="flex items-center gap-2 text-sm"><input v-model="ex.silent" type="checkbox" /> {{ t('privacy.export.noNotify') }}</label>
    <TextField v-if="ex.silent" v-model="ex.reason" :label="t('privacy.export.reason')" :help="t('privacy.export.reasonHelp')" autocomplete="off" />
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>
</template>
