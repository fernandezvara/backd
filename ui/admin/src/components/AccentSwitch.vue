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
  <div class="flex items-center gap-2 text-sm" role="group" :aria-label="t('accent.label')">
    <span v-if="!compact" class="shrink-0">{{ t('accent.label') }}</span>
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
