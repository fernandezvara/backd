<script setup lang="ts">
import { PhCopy } from '@phosphor-icons/vue'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigTabs from '@/components/ConfigTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useAdmin } from '@/lib/admin'
import { useRawAdmin } from '@/lib/adminRaw'
import { downloadText } from '@/lib/draft'
import { errorText } from '@/lib/format'
import { useToasts } from '@/stores/toasts'

// Backup and the API files. A backup is made with the `backd` command (it streams collections and files,
// which a browser tab should not carry); this page gives the commands for this realm. The API files are
// the documents and types the server generates from the configuration it runs.
const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const raw = useRawAdmin()
const toasts = useToasts()
const busy = ref('')

const commands = [
  { id: 'export', label: 'config.backup.export', text: `backd export --realm ${props.realm} --include auth --out ./${props.realm}-backup --archive` },
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

async function file(kind: 'admin' | 'client' | 'ts' | 'dart' | 'go' | 'python') {
  busy.value = kind
  try {
    if (kind === 'admin' || kind === 'client') {
      const doc = kind === 'client' ? await admin.clientOpenapi() : await admin.openapi()
      downloadText(`${props.realm}-openapi-${kind}.json`, JSON.stringify(doc, null, 2), 'application/json')
    } else {
      const r = await raw<{ types: string }>('GET', ['types'], { query: { lang: kind } })
      downloadText({ ts: 'types.d.ts', dart: 'types.dart', go: 'types.go', python: 'types.py' }[kind], r.types)
    }
    toasts.push(t('config.backup.downloaded'))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    busy.value = ''
  }
}
const files = [
  { id: 'client', label: 'config.backup.files.client' },
  { id: 'admin', label: 'config.backup.files.admin' },
  { id: 'ts', label: 'config.backup.files.ts' },
  { id: 'dart', label: 'config.backup.files.dart' },
  { id: 'go', label: 'config.backup.files.go' },
  { id: 'python', label: 'config.backup.files.python' },
] as const
</script>

<template>
  <PageHeader :title="t('config.backup.title')" :description="t('config.backup.description')" />
  <ConfigTabs />

  <section class="mb-6" aria-labelledby="backup-h">
    <h2 id="backup-h" class="mb-2 text-sm font-semibold">{{ t('config.backup.backup') }}</h2>
    <AppCard>
      <p class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ t('config.backup.help') }}</p>
      <div class="space-y-3">
        <div v-for="c in commands" :key="c.id">
          <p class="mb-1 text-xs font-medium text-slate-500">{{ t(c.label) }}</p>
          <div class="flex items-center gap-2 rounded-md bg-slate-900 p-2 pl-3 text-slate-100">
            <code class="min-w-0 flex-1 overflow-x-auto font-mono text-xs whitespace-nowrap" :data-testid="`cmd-${c.id}`">{{ c.text }}</code>
            <button type="button" class="grid size-7 place-items-center rounded text-slate-300 hover:bg-white/10" :aria-label="t('common.copy')" @click="copy(c.text)"><PhCopy :size="15" aria-hidden="true" /></button>
          </div>
        </div>
      </div>
      <p class="mt-3 text-xs text-slate-500">{{ t('config.backup.note') }}</p>
    </AppCard>
  </section>

  <section aria-labelledby="files-h">
    <h2 id="files-h" class="mb-2 text-sm font-semibold">{{ t('config.backup.filesTitle') }}</h2>
    <AppCard>
      <p class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ t('config.backup.filesHelp') }}</p>
      <div class="flex flex-wrap gap-2">
        <AppButton v-for="f in files" :key="f.id" variant="secondary" :busy="busy === f.id" :data-testid="`file-${f.id}`" @click="file(f.id)">{{ t(f.label) }}</AppButton>
      </div>
    </AppCard>
  </section>
</template>
