<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ConfigFrame from '@/components/ConfigFrame.vue'

defineProps<{ realm: string }>()
const { t } = useI18n()
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.overview')">
    <template #default="{ config }">
      <dl class="grid max-w-3xl grid-cols-[10rem_1fr] gap-x-4 gap-y-2 text-sm">
        <dt class="text-slate-600 dark:text-slate-400">{{ t('config.realm') }}</dt>
        <dd class="font-medium" data-testid="config-realm">{{ config.realm }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('config.realmFile') }}</dt>
        <dd class="font-mono text-xs">{{ config.file }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('config.fingerprint') }}</dt>
        <dd>
          <code class="font-mono text-xs break-all" data-testid="config-fingerprint">{{ config.fingerprint }}</code>
          <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('config.fingerprintHelp') }}</p>
        </dd>
      </dl>

      <section class="mt-8" aria-labelledby="warnings-h">
        <h2 id="warnings-h" class="text-lg font-semibold">{{ t('config.warnings.title') }}</h2>
        <p v-if="!config.warnings.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('config.warnings.none') }}</p>
        <ul v-else class="mt-2 space-y-2" data-testid="config-warnings">
          <li v-for="w in config.warnings" :key="w.code" class="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100">
            <code class="font-mono text-xs">{{ w.code }}</code>
            <span class="ml-2">{{ w.message }}</span>
          </li>
        </ul>
      </section>
    </template>
  </ConfigFrame>
</template>
