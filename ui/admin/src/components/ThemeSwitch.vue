<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { usePreferences, type Theme } from '@/stores/preferences'

const { t } = useI18n()
const prefs = usePreferences()
const themes: Theme[] = ['system', 'light', 'dark']
</script>

<template>
  <label class="flex items-center gap-2 text-sm">
    <span class="sr-only">{{ t('theme.label') }}</span>
    <select v-model="prefs.theme" class="rounded-md border border-slate-300 bg-white px-2 py-1 text-sm dark:border-slate-600 dark:bg-slate-900" :aria-label="t('theme.label')">
      <option v-for="theme in themes" :key="theme" :value="theme">{{ t(`theme.${theme}`) }}</option>
    </select>
  </label>
</template>
