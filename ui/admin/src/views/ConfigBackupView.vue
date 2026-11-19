<script setup lang="ts">
import { PhCopy, PhExport, PhUploadSimple } from '@phosphor-icons/vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigTabs from '@/components/ConfigTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useToasts } from '@/stores/toasts'

// A backup is made with the `backd` command: it streams every collection and file, which a browser tab
// should not carry. This page gives the commands for this realm.
const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const toasts = useToasts()

const exportCmds = [
  { id: 'export', label: 'config.backup.export', text: `backd export --realm ${props.realm} --include auth --out ./${props.realm}-backup --archive` },
]
const importCmds = [
  { id: 'dry', label: 'config.backup.dryRun', text: `backd import --realm ${props.realm} --from ./${props.realm}-backup.tar.gz --dry-run` },
  { id: 'import', label: 'config.backup.import', text: `backd import --realm ${props.realm} --from ./${props.realm}-backup.tar.gz` },
]
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push(t('common.copied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}
</script>

<template>
  <PageHeader :title="t('config.title')" :description="t('config.help')" />
  <ConfigTabs />

  <AppAlert class="mb-4">{{ t('config.backup.help') }}</AppAlert>
  <div class="space-y-5">
    <section aria-labelledby="export-h">
      <AppCard>
        <h2 id="export-h" class="mb-1 flex items-center gap-2 text-sm font-semibold"><PhExport :size="16" aria-hidden="true" />{{ t('config.backup.exportTitle') }}</h2>
        <p class="mb-3 text-[13px] text-slate-600 dark:text-slate-400">{{ t('config.backup.exportHelp') }}</p>
        <div class="space-y-3">
          <div v-for="c in exportCmds" :key="c.id">
            <p class="mb-1 text-xs font-medium text-slate-500">{{ t(c.label) }}</p>
            <div class="flex items-center gap-2 rounded-md bg-slate-900 p-2 pl-3 text-slate-100">
              <code class="min-w-0 flex-1 overflow-x-auto font-mono text-xs whitespace-nowrap" :data-testid="`cmd-${c.id}`">{{ c.text }}</code>
              <button type="button" class="grid size-7 place-items-center rounded text-slate-300 hover:bg-white/10" :aria-label="t('common.copy')" @click="copy(c.text)"><PhCopy :size="15" aria-hidden="true" /></button>
            </div>
          </div>
        </div>
        <ul class="mt-3 list-disc space-y-1 pl-5 text-xs text-slate-600 dark:text-slate-400">
          <li>{{ t('config.backup.includes') }}</li>
          <li>{{ t('config.backup.note') }}</li>
        </ul>
      </AppCard>
    </section>
    <section aria-labelledby="import-h">
      <AppCard>
        <h2 id="import-h" class="mb-1 flex items-center gap-2 text-sm font-semibold"><PhUploadSimple :size="16" aria-hidden="true" />{{ t('config.backup.importTitle') }}</h2>
        <p class="mb-3 text-[13px] text-slate-600 dark:text-slate-400">{{ t('config.backup.importHelp') }}</p>
        <div class="space-y-3">
          <div v-for="c in importCmds" :key="c.id">
            <p class="mb-1 text-xs font-medium text-slate-500">{{ t(c.label) }}</p>
            <div class="flex items-center gap-2 rounded-md bg-slate-900 p-2 pl-3 text-slate-100">
              <code class="min-w-0 flex-1 overflow-x-auto font-mono text-xs whitespace-nowrap" :data-testid="`cmd-${c.id}`">{{ c.text }}</code>
              <button type="button" class="grid size-7 place-items-center rounded text-slate-300 hover:bg-white/10" :aria-label="t('common.copy')" @click="copy(c.text)"><PhCopy :size="15" aria-hidden="true" /></button>
            </div>
          </div>
        </div>
        <p class="mt-3 text-xs text-slate-600 dark:text-slate-400">{{ t('config.backup.importNote') }}</p>
      </AppCard>
    </section>
  </div>
</template>
