<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { LOCALES, LOCALE_NAMES } from '@/i18n/locale'
import { usePreferences } from '@/stores/preferences'

const { t } = useI18n()
const prefs = usePreferences()
</script>

<template>
  <label class="flex items-center gap-2 text-sm">
    <span class="sr-only">{{ t('language.label') }}</span>
    <select v-model="prefs.language" class="rounded-md border border-slate-300 bg-white px-2 py-1 text-sm dark:border-slate-600 dark:bg-slate-900" :aria-label="t('language.label')" data-testid="language-switch">
      <option value="auto">{{ t('language.auto') }}</option>
      <!-- Each language in its own words, whatever the current one is. -->
      <option v-for="l in LOCALES" :key="l" :value="l" :lang="l">{{ LOCALE_NAMES[l] }}</option>
    </select>
  </label>
</template>
