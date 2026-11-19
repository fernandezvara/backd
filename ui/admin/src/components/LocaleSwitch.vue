<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { LOCALES, LOCALE_NAMES } from '@/i18n/locale'
import { usePreferences } from '@/stores/preferences'
import SegmentedControl from './SegmentedControl.vue'

// The language: the browser's, or one of the offered, each named in its own words. Kept in this browser.
const { t } = useI18n()
const prefs = usePreferences()
const options = computed(() => [
  { value: 'auto' as const, label: t('language.auto'), testid: 'language-auto' },
  ...LOCALES.map((l) => ({ value: l, label: l.toUpperCase(), title: LOCALE_NAMES[l], testid: `language-${l}` })),
])
</script>

<template>
  <SegmentedControl v-model="prefs.language" :label="t('language.label')" :options="options" data-testid="language-switch" />
</template>
