<script setup lang="ts">
import { PhCopy, PhDownloadSimple, PhFileCode, PhFileTs } from '@phosphor-icons/vue'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigTabs from '@/components/ConfigTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useAdmin } from '@/lib/admin'
import { useRawAdmin } from '@/lib/adminRaw'
import { downloadText } from '@/lib/download'
import { errorText } from '@/lib/format'
import { useToasts } from '@/stores/toasts'

// The documents and types the server generates from the configuration it runs. Their routes need the
// bearer token, so they are downloaded from here rather than opened as links.
const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const raw = useRawAdmin()
const toasts = useToasts()
const busy = ref('')

type Lang = 'ts' | 'dart' | 'go' | 'python'
const lang = ref<Lang>('ts')
const origin = window.location.origin
const docs = [
  { id: 'admin' as const, label: 'config.api.admin', path: `/v1/${props.realm}/_admin/openapi.json` },
  { id: 'client' as const, label: 'config.api.client', path: `/v1/${props.realm}/_openapi.json` },
]

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push(t('common.copied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}
async function download(kind: 'admin' | 'client' | 'types') {
  busy.value = kind
  try {
    if (kind === 'types') {
      const r = await raw<{ types: string }>('GET', ['types'], { query: { lang: lang.value } })
      downloadText({ ts: 'types.d.ts', dart: 'types.dart', go: 'types.go', python: 'types.py' }[lang.value], r.types)
    } else {
      const doc = kind === 'client' ? await admin.clientOpenapi() : await admin.openapi()
      downloadText(`${props.realm}-openapi-${kind}.json`, JSON.stringify(doc, null, 2), 'application/json')
    }
    toasts.push(t('config.backup.downloaded'))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <PageHeader :title="t('config.title')" :description="t('config.help')" />
  <ConfigTabs />

  <p class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ t('config.api.help') }}</p>
  <AppCard flush>
    <ul class="divide-y divide-slate-100 dark:divide-slate-800">
      <li v-for="d in docs" :key="d.id" class="flex flex-wrap items-center gap-3 p-4">
        <PhFileCode :size="22" class="text-slate-400" aria-hidden="true" />
        <div class="min-w-0 flex-1">
          <b class="text-[13px]">{{ t(d.label) }}</b>
          <div class="truncate font-mono text-xs text-slate-600 dark:text-slate-400">{{ origin }}{{ d.path }}</div>
        </div>
        <AppButton variant="secondary" @click="copy(origin + d.path)"><PhCopy :size="14" aria-hidden="true" />{{ t('config.api.copyUrl') }}</AppButton>
        <AppButton variant="secondary" :busy="busy === d.id" :data-testid="`file-${d.id}`" @click="download(d.id)"><PhDownloadSimple :size="14" aria-hidden="true" />{{ t('config.api.download') }}</AppButton>
      </li>
      <li class="flex flex-wrap items-center gap-3 p-4">
        <PhFileTs :size="22" class="text-slate-400" aria-hidden="true" />
        <div class="min-w-0 flex-1">
          <b class="text-[13px]">{{ t('config.api.types') }}</b>
          <div class="text-xs text-slate-600 dark:text-slate-400">{{ t('config.api.typesHelp') }}</div>
        </div>
        <select v-model="lang" class="h-8 rounded-md border border-slate-300 bg-white px-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="t('config.api.language')" data-testid="types-lang">
          <option value="ts">TypeScript</option>
          <option value="python">Python</option>
          <option value="go">Go</option>
          <option value="dart">Dart</option>
        </select>
        <AppButton variant="secondary" :busy="busy === 'types'" data-testid="file-types" @click="download('types')"><PhDownloadSimple :size="14" aria-hidden="true" />{{ t('config.api.download') }}</AppButton>
      </li>
    </ul>
  </AppCard>
</template>
