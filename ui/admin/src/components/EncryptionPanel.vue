<script setup lang="ts">
import type { Admin } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import ProofField from '@/components/ProofField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// Encrypted fields: how each is stored now (plain, sealed with the current data key, sealed with an old
// one), a new data-key generation, and the migration job that seals everything again.
type EncryptionStatus = Awaited<ReturnType<Admin['encryption']['status']>>[number]
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const proof = useProof()
const full = computed(() => session.access?.level === 'full' && session.canWrite('data'))

const rows = ref<EncryptionStatus[]>([])
const loading = ref(true)
const loadError = ref('')
async function load() {
  loadError.value = ''
  try {
    rows.value = await admin.encryption.status()
  } catch (e) {
    loadError.value = errorText(e, t('encryption.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

type Action = { kind: 'rekey' | 'migrate'; database: string; collection: string; field?: string }
const action = ref<Action | null>(null)
const dryRun = ref(true)
const busy = ref(false)
const error = ref('')
function ask(a: Action) {
  action.value = a
  dryRun.value = true
  error.value = ''
  void proof.prepare()
}
async function run() {
  const a = action.value!
  busy.value = true
  error.value = ''
  try {
    await proof.prove()
    if (a.kind === 'rekey') {
      const r = await admin.encryption.rekey({ database: a.database, collection: a.collection, field: a.field! })
      toasts.push(t('encryption.rekeyed', { field: a.field ?? '', generation: r.generation }))
    } else {
      await admin.encryption.migrate({ database: a.database, collection: a.collection, dryRun: dryRun.value })
      toasts.push(t(dryRun.value ? 'encryption.dryStarted' : 'encryption.started', { collection: `${a.database}/${a.collection}` }))
    }
    action.value = null
    await load()
  } catch (e) {
    error.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppAlert v-if="loadError" kind="error">{{ loadError }}</AppAlert>
  <p v-else-if="loading" role="status" class="text-sm">{{ t('common.loading') }}</p>
  <p v-else-if="!rows.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('encryption.none') }}</p>
  <div v-else class="space-y-4" data-testid="encryption">
    <AppCard v-for="c in rows" :key="c.database + c.collection" flush>
      <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-2.5 dark:border-slate-800">
        <b class="font-mono text-[13px]">{{ c.database }}/{{ c.collection }}</b>
        <AppBadge :kind="c.complete ? 'ok' : 'warn'">{{ c.complete ? t('encryption.complete') : t('encryption.pending') }}</AppBadge>
        <AppButton v-if="full" variant="secondary" class="ml-auto" @click="ask({ kind: 'migrate', database: c.database, collection: c.collection })">{{ t('encryption.migrate') }}</AppButton>
      </div>
      <table class="w-full text-left text-[13px]">
        <thead><tr><th class="px-4 py-1.5 font-medium">{{ t('encryption.field') }}</th><th class="px-3 py-1.5 font-medium">{{ t('encryption.key') }}</th><th class="px-3 py-1.5 font-medium">{{ t('encryption.plain') }}</th><th class="px-3 py-1.5 font-medium">{{ t('encryption.current') }}</th><th class="px-3 py-1.5 font-medium">{{ t('encryption.old') }}</th><th class="px-3 py-1.5" /></tr></thead>
        <tbody>
          <tr v-for="f in c.fields" :key="f.path" class="border-t border-slate-100 dark:border-slate-800">
            <td class="px-4 py-1.5 font-mono">{{ f.path }}<AppBadge v-if="!f.configured" kind="warn" class="ml-1">{{ t('encryption.notConfigured') }}</AppBadge><AppBadge v-if="f.search" class="ml-1">{{ t('encryption.search') }}</AppBadge></td>
            <td class="px-3 py-1.5 font-mono text-xs text-slate-500">{{ f.key ?? '—' }}</td>
            <td class="px-3 py-1.5">{{ f.plaintext }}</td>
            <td class="px-3 py-1.5">{{ f.current }}</td>
            <td class="px-3 py-1.5">{{ f.old }}</td>
            <td class="px-3 py-1.5 text-right"><AppButton v-if="full && f.configured" variant="secondary" class="!h-7 !px-2 !text-xs" @click="ask({ kind: 'rekey', database: c.database, collection: c.collection, field: f.path })">{{ t('encryption.rekey') }}</AppButton></td>
          </tr>
        </tbody>
      </table>
    </AppCard>
  </div>
  <p v-if="!full && rows.length" class="mt-3 text-xs text-slate-500">{{ t('encryption.needsFull') }}</p>

  <FormDialog :open="!!action" :title="t(action?.kind === 'rekey' ? 'encryption.rekeyTitle' : 'encryption.migrateTitle', { name: action?.field ?? `${action?.database}/${action?.collection}` })" :description="t(action?.kind === 'rekey' ? 'encryption.rekeyBody' : 'encryption.migrateBody')" :submit="t(action?.kind === 'rekey' ? 'encryption.rekey' : 'encryption.migrate')" :busy="busy" :error="error" @update:open="(v: boolean) => !v && (action = null)" @submit="run">
    <label v-if="action?.kind === 'migrate'" class="flex items-center gap-2 text-sm"><input v-model="dryRun" type="checkbox" /> {{ t('encryption.dry') }}</label>
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>
</template>
