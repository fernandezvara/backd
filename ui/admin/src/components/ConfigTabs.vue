<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import TabNav from './TabNav.vue'

const { t } = useI18n()
const tabs = [
  { name: 'config', label: 'config.tabs.overview' },
  { name: 'config-settings', label: 'config.tabs.settings' },
  { name: 'config-collections', label: 'config.tabs.collections' },
  { name: 'config-templates', label: 'config.tabs.templates' },
]
</script>

<template>
  <TabNav :label="t('config.tabs.label')" :tabs="tabs" />
</template>
