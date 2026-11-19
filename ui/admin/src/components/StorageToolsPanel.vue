<script setup lang="ts">
import type { StorageCheck, StorageReconcile } from 'backd-js'
import { PhArrowsClockwise, PhBroom, PhPlugsConnected, PhWrench } from '@phosphor-icons/vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// What an operator does with the storage: verify that it works, find files no document references (and
// remove them), and make image versions again after the declaration changed.
const props = defineProps<{ publicToo: boolean }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()
const canWrite = computed(() => session.canWrite('config'))

const which = ref<'private' | 'public'>('private')
const busy = ref('')
const error = ref('')
const check = ref<StorageCheck>()
const reconcile = ref<StorageReconcile>()

async function runCheck() {
  busy.value = 'check'
  error.value = ''
  try {
    check.value = await admin.storage.check({ storage: which.value })
  } catch (e) {
    error.value = errorText(e, t('storage.tools.failed'))
  } finally {
    busy.value = ''
  }
}
async function runReconcile(remove: boolean) {
  busy.value = 'reconcile'
  error.value = ''
  try {
    reconcile.value = await admin.storage.reconcile({ delete: remove, storage: which.value })
    if (remove) toasts.push(t('storage.tools.deleted', { n: reconcile.value.deleted }))
  } catch (e) {
    error.value = errorText(e, t('storage.tools.failed'))
  } finally {
    busy.value = ''
  }
}
const levelKind = (l: string) => (l === 'ok' ? 'ok' : l === 'warn' ? 'warn' : l === 'fail' ? 'error' : 'neutral')

// Regenerate image versions
const regen = ref(false)
const form = ref({ database: '', collection: '', field: '', version: '', missingOnly: true })
const regenError = ref('')
async function regenerate() {
  regenError.value = ''
  const f = form.value
  if (!f.database.trim() || !f.collection.trim() || !f.field.trim()) {
    regenError.value = t('storage.tools.regenRequired')
    return
  }
  busy.value = 'regen'
  try {
    await admin.storage.regenerateVersions({ database: f.database.trim(), collection: f.collection.trim(), field: f.field.trim(), version: f.version.trim() || undefined, missingOnly: f.missingOnly })
    toasts.push(t('storage.tools.regenStarted'))
    regen.value = false
  } catch (e) {
    regenError.value = errorText(e, t('common.unexpected'))
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <section class="mt-6" aria-labelledby="tools-h" data-testid="storage-tools">
    <h2 id="tools-h" class="mb-2 flex items-center gap-2 text-[13px] font-semibold"><PhWrench :size="15" class="text-slate-400" aria-hidden="true" />{{ t('storage.tools.title') }}</h2>
    <AppCard>
      <div class="flex flex-wrap items-center gap-2">
        <select v-if="props.publicToo" v-model="which" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('storage.tools.which')">
          <option value="private">{{ t('storage.tools.private') }}</option>
          <option value="public">{{ t('storage.tools.public') }}</option>
        </select>
        <AppButton variant="secondary" :busy="busy === 'check'" @click="runCheck"><PhPlugsConnected :size="15" aria-hidden="true" />{{ t('storage.tools.check') }}</AppButton>
        <AppButton v-if="canWrite" variant="secondary" :busy="busy === 'reconcile'" @click="runReconcile(false)"><PhBroom :size="15" aria-hidden="true" />{{ t('storage.tools.reconcile') }}</AppButton>
        <AppButton v-if="canWrite" variant="secondary" @click="((regen = true), (regenError = ''))"><PhArrowsClockwise :size="15" aria-hidden="true" />{{ t('storage.tools.regen') }}</AppButton>
      </div>
      <p class="mt-2 text-xs text-slate-500">{{ t('storage.tools.help') }}</p>
      <AppAlert v-if="error" kind="error" class="mt-3">{{ error }}</AppAlert>

      <div v-if="check" class="mt-4" data-testid="check-report">
        <p class="mb-1 flex items-center gap-2 text-[13px] font-medium"><AppBadge :kind="check.ok ? 'ok' : 'error'">{{ check.ok ? t('storage.tools.pass') : t('storage.tools.fail') }}</AppBadge> {{ check.provider }} · {{ check.bucket }}/{{ check.prefix }}</p>
        <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
          <li v-for="s in check.steps" :key="s.name" class="flex items-start gap-2 py-1.5"><AppBadge :kind="levelKind(s.level)">{{ s.level }}</AppBadge><span class="font-medium">{{ s.name }}</span><span class="text-slate-500">{{ s.detail }}</span></li>
        </ul>
        <p v-if="check.sentinel_url" class="mt-2 text-xs break-all">{{ t('storage.tools.sentinel') }} <a :href="check.sentinel_url" class="underline" target="_blank" rel="noreferrer">{{ check.sentinel_url }}</a></p>
      </div>

      <div v-if="reconcile" class="mt-4 text-[13px]" data-testid="reconcile-report">
        <p>{{ t('storage.tools.scanned', { scanned: reconcile.scanned, referenced: reconcile.referenced, orphans: reconcile.orphans.length }) }}</p>
        <p v-if="reconcile.skipped_recent || reconcile.skipped_in_journal" class="text-xs text-slate-500">{{ t('storage.tools.skipped', { n: reconcile.skipped_recent + reconcile.skipped_in_journal }) }}</p>
        <ul v-if="reconcile.orphans.length" class="mt-2 max-h-48 overflow-auto rounded-md border border-slate-200 p-2 font-mono text-xs dark:border-slate-800"><li v-for="o in reconcile.orphans" :key="o.key">{{ o.key }}</li></ul>
        <AppButton v-if="canWrite && !reconcile.delete && reconcile.orphans.length" variant="danger" class="mt-3" :busy="busy === 'reconcile'" @click="runReconcile(true)">{{ t('storage.tools.removeOrphans', { n: reconcile.orphans.length }) }}</AppButton>
        <p v-if="reconcile.delete" class="mt-2">{{ t('storage.tools.deleted', { n: reconcile.deleted }) }}<template v-if="reconcile.failed.length"> · {{ t('storage.tools.failedN', { n: reconcile.failed.length }) }}</template></p>
      </div>
    </AppCard>
  </section>

  <FormDialog v-model:open="regen" :title="t('storage.tools.regenTitle')" :description="t('storage.tools.regenBody')" :submit="t('storage.tools.regenSubmit')" :busy="busy === 'regen'" :error="regenError" @submit="regenerate">
    <TextField v-model="form.database" :label="t('storage.tools.database')" autocomplete="off" required />
    <TextField v-model="form.collection" :label="t('storage.tools.collection')" autocomplete="off" required />
    <TextField v-model="form.field" :label="t('storage.tools.field')" autocomplete="off" required />
    <TextField v-model="form.version" :label="t('storage.tools.version')" :help="t('storage.tools.versionHelp')" autocomplete="off" />
    <label class="flex items-center gap-2 text-sm"><input v-model="form.missingOnly" type="checkbox" /> {{ t('storage.tools.missingOnly') }}</label>
  </FormDialog>
</template>
