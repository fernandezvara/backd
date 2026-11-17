<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import TabNav from './TabNav.vue'

const { t } = useI18n()
const tabs = [
  { name: 'config', label: 'config.tabs.overview' },
  { name: 'config-settings', label: 'config.tabs.settings' },
  { name: 'config-signin', label: 'config.tabs.signin' },
  { name: 'config-collections', label: 'config.tabs.collections' },
  { name: 'config-templates', label: 'config.tabs.templates' },
  { name: 'config-files', label: 'config.tabs.files' },
  { name: 'config-backup', label: 'config.tabs.backup' },
  { name: 'config-api', label: 'config.tabs.api' },
]
</script>

<template>
  <TabNav :label="t('config.tabs.label')" :tabs="tabs" />
</template>
