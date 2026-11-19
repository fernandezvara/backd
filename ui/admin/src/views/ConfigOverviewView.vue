<script setup lang="ts">
import { PhCopy } from '@phosphor-icons/vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import AppCard from '@/components/AppCard.vue'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { useRealmConfig } from '@/stores/config'
import { useToasts } from '@/stores/toasts'

// The realm at a glance: what is loaded, and the warnings the server gave. The files themselves are in Files.
defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()
const toasts = useToasts()

const counts = computed(() => {
  const dbs = Object.values((cfg.config?.databases ?? {}) as Record<string, { collections: unknown[]; functions: unknown[] }>)
  return { databases: dbs.length, collections: dbs.reduce((n, d) => n + d.collections.length, 0), functions: dbs.reduce((n, d) => n + d.functions.length, 0) }
})

// The fingerprint is long: its last 8 characters tell instances apart, and the button copies all of it.
const short = (fp: string) => fp.slice(-8)
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push(t('config.overview.fingerprintCopied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.overview')">
    <template #default="{ config }">
      <section aria-labelledby="status-h">
        <AppCard flush>
          <h2 id="status-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('config.overview.status') }}</h2>
          <dl class="grid divide-y divide-slate-100 text-[13px] sm:grid-cols-2 sm:divide-y-0 lg:grid-cols-3 dark:divide-slate-800">
            <div class="flex items-center justify-between gap-3 border-slate-100 px-4 py-2.5 sm:border-b dark:border-slate-800"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.realm') }}</dt><dd class="font-medium" data-testid="config-realm">{{ config.realm }}</dd></div>
            <div class="flex items-center justify-between gap-3 border-slate-100 px-4 py-2.5 sm:border-b dark:border-slate-800">
              <dt class="text-slate-600 dark:text-slate-400">{{ t('config.fingerprint') }}</dt>
              <dd class="flex items-center gap-1.5">
                <code class="font-mono text-xs" data-testid="config-fingerprint">{{ short(config.fingerprint) }}</code>
                <button type="button" class="grid size-6 place-items-center rounded text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800" :aria-label="t('config.overview.copyFingerprint')" :title="t('config.overview.copyFingerprint')" data-testid="copy-fingerprint" @click="copy(config.fingerprint)"><PhCopy :size="14" aria-hidden="true" /></button>
              </dd>
            </div>
            <div class="flex items-center justify-between gap-3 border-slate-100 px-4 py-2.5 sm:border-b dark:border-slate-800"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.realmFile') }}</dt><dd class="font-mono text-xs">{{ config.file }}</dd></div>
            <div class="flex items-center justify-between gap-3 border-slate-100 px-4 py-2.5 sm:border-b dark:border-slate-800"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.overview.databases') }}</dt><dd>{{ counts.databases }}</dd></div>
            <div class="flex items-center justify-between gap-3 border-slate-100 px-4 py-2.5 sm:border-b dark:border-slate-800"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.overview.collections') }}</dt><dd>{{ counts.collections }}</dd></div>
            <div class="flex items-center justify-between gap-3 px-4 py-2.5"><dt class="text-slate-600 dark:text-slate-400">{{ t('nav.functions') }}</dt><dd>{{ counts.functions }}</dd></div>
          </dl>
          <p class="border-t border-slate-200 p-3 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-400">{{ t('config.fingerprintHelp') }}</p>
        </AppCard>
      </section>

      <section class="mt-6" aria-labelledby="warnings-h">
        <h2 id="warnings-h" class="mb-2 text-sm font-semibold">{{ t('config.warnings.title') }}</h2>
        <p v-if="!config.warnings.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.warnings.none') }}</p>
        <ul v-else class="space-y-2" data-testid="config-warnings">
          <li v-for="w in config.warnings" :key="w.code" class="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100">
            <code class="font-mono text-xs">{{ w.code }}</code>
            <span class="ml-2">{{ w.message }}</span>
          </li>
        </ul>
      </section>
    </template>
  </ConfigFrame>
</template>
