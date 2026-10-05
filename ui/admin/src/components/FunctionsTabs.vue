<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import TabNav from './TabNav.vue'

const { t } = useI18n()
const tabs = [
  { name: 'functions', label: 'functions.tabs.definitions' },
  { name: 'functions-history', label: 'functions.tabs.history' },
  { name: 'functions-jobs', label: 'functions.tabs.jobs' },
]
</script>

<template>
  <TabNav :label="t('functions.tabs.label')" :tabs="tabs" />
</template>
