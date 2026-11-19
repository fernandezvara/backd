<script setup lang="ts">
import { PhDesktop, PhMoon, PhSun } from '@phosphor-icons/vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usePreferences, type Theme } from '@/stores/preferences'

// The quick switch of the top bar: system, light, dark in turn (the Account page has the full choice).
const { t } = useI18n()
const prefs = usePreferences()
const ORDER: Theme[] = ['system', 'light', 'dark']
const icon = computed(() => ({ system: PhDesktop, light: PhSun, dark: PhMoon })[prefs.theme])
function next() {
  prefs.theme = ORDER[(ORDER.indexOf(prefs.theme) + 1) % ORDER.length]
}
</script>

<template>
  <button type="button" class="grid size-8 place-items-center rounded-md text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800" :aria-label="`${t('theme.label')}: ${t(`theme.${prefs.theme}`)}`" :title="`${t('theme.label')}: ${t(`theme.${prefs.theme}`)}`" data-testid="theme-toggle" @click="next">
  <component :is="icon" :size="18" aria-hidden="true" /></button>
</template>
