<script setup lang="ts">
import { PhDesktop, PhMoon, PhSun } from '@phosphor-icons/vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usePreferences, type Theme } from '@/stores/preferences'
import SegmentedControl from './SegmentedControl.vue'

// The theme: follow the system, or light, or dark. Kept in this browser.
defineProps<{ compact?: boolean }>()
const { t } = useI18n()
const prefs = usePreferences()
const options = computed(() => (['system', 'light', 'dark'] as Theme[]).map((v) => ({ value: v, label: t(`theme.${v}`), icon: { system: PhDesktop, light: PhSun, dark: PhMoon }[v], testid: `theme-${v}` })))
</script>

<template>
  <SegmentedControl v-model="prefs.theme" :label="t('theme.label')" :options="options" :icon-only="compact" />
</template>
