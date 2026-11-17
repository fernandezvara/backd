<script setup lang="ts">
import type { ConfigFile } from 'backd-js'
import { PhMagnifyingGlass } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigTabs from '@/components/ConfigTabs.vue'
import FileTree from '@/components/FileTree.vue'
import PageHeader from '@/components/PageHeader.vue'
import SourceViewer from '@/components/SourceViewer.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'

// Every file of the realm's configuration, read-only: the whole structure, to inspect what the server
// loaded. Changes are made in the configuration repository, never here.
defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()

const files = ref<ConfigFile[]>([])
const omitted = ref(0)
const loaded = ref(false)
const error = ref('')
const selected = ref('')
const filter = ref('')

onMounted(async () => {
  try {
    const out = await admin.configFiles()
    files.value = out.files
    omitted.value = out.omitted
    selected.value = (out.files.find((f) => f.role === 'realm') ?? out.files[0])?.path ?? ''
  } catch (e) {
    error.value = errorText(e, t('config.files.loadFailed'))
  } finally {
    loaded.value = true
  }
})

const paths = computed(() => files.value.map((f) => f.path))
const file = computed(() => files.value.find((f) => f.path === selected.value))
const roleKind = (r: string) => (r === 'realm' || r === 'realm_config' ? 'violet' : r === 'function' || r === 'migration' ? 'blue' : 'neutral')
const where = computed(() => {
  const f = file.value
  if (!f) return ''
  return [f.database, f.collection].filter(Boolean).join(' / ')
})
</script>

<template>
  <PageHeader :title="t('config.title')" :description="t('config.help')" />
  <ConfigTabs />

  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <p v-else-if="!loaded" class="text-sm" role="status">{{ t('config.loading') }}</p>
  <template v-else>
    <p class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ t('config.files.intro', { n: files.length }) }}</p>
    <div class="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)]">
      <AppCard flush class="h-fit">
        <div class="relative border-b border-slate-200 p-2 dark:border-slate-800">
          <PhMagnifyingGlass :size="14" class="pointer-events-none absolute left-4.5 top-4.5 text-slate-400" aria-hidden="true" />
          <input v-model="filter" type="search" class="h-8 w-full rounded-md border border-slate-300 bg-white pl-8 pr-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :placeholder="t('config.files.filter')" :aria-label="t('config.files.filter')" autocomplete="off" />
        </div>
        <div class="max-h-[40rem] overflow-auto">
          <FileTree v-model="selected" :paths="paths" :filter="filter" :label="t('config.files.tree')" :open-depth="2" />
        </div>
        <p v-if="omitted" class="border-t border-slate-200 p-2 text-xs text-slate-500 dark:border-slate-800">{{ t('config.files.omitted', { n: omitted }) }}</p>
      </AppCard>
      <div class="min-w-0 space-y-2">
        <div v-if="file" class="flex flex-wrap items-center gap-2 text-xs text-slate-600 dark:text-slate-400">
          <AppBadge :kind="roleKind(file.role)">{{ t(`config.files.roles.${file.role}`) }}</AppBadge>
          <span v-if="where">{{ where }}</span>
          <span>· {{ file.size }} B</span>
          <span class="font-mono">· {{ file.sha256.slice(0, 8) }}</span>
        </div>
        <SourceViewer :name="file?.path ?? ''" :file="file" :hint="t('config.files.hint')" />
      </div>
    </div>
  </template>
</template>
