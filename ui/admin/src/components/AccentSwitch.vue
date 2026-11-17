<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ACCENTS, usePreferences } from '@/stores/preferences'

// The accent colour of the interface: a row of swatches, kept in this browser.
defineProps<{ compact?: boolean }>()
const { t } = useI18n()
const prefs = usePreferences()
const color = (a: string) => `var(--sw-${a})`
</script>

<template>
  <div v-if="!compact" class="flex flex-wrap gap-2" role="group" :aria-label="t('accent.label')">
    <button
      v-for="a in ACCENTS"
      :key="a"
      type="button"
      class="flex h-8 items-center gap-2 rounded-full border px-3 text-sm"
      :class="prefs.accent === a ? 'border-brand-600 bg-brand-50 font-medium text-brand-800 dark:border-brand-400 dark:bg-brand-900/30 dark:text-brand-100' : 'border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800'"
      :aria-pressed="prefs.accent === a"
      :data-testid="`accent-${a}`"
      @click="prefs.accent = a"
    >
      <span class="size-3.5 rounded-full" :style="{ background: color(a) }" aria-hidden="true" />{{ t(`accent.names.${a}`) }}
    </button>
  </div>
  <div v-else class="flex items-center gap-2 text-sm" role="group" :aria-label="t('accent.label')">
    <span class="ml-auto flex gap-1.5">
      <button
        v-for="a in ACCENTS"
        :key="a"
        type="button"
        class="swatch"
        :style="{ background: color(a) }"
        :aria-pressed="prefs.accent === a"
        :aria-label="t(`accent.names.${a}`)"
        :title="t(`accent.names.${a}`)"
        :data-testid="`accent-${a}`"
        @click="prefs.accent = a"
      />
    </span>
  </div>
</template>
