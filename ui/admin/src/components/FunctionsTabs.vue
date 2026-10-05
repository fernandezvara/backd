<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'

const { t } = useI18n()
const session = useSession()
const tabs = [
  { name: 'functions', label: 'functions.tabs.definitions' },
  { name: 'functions-history', label: 'functions.tabs.history' },
  { name: 'functions-jobs', label: 'functions.tabs.jobs' },
]
</script>

<template>
  <nav :aria-label="t('functions.tabs.label')" class="mb-4">
    <ul class="flex gap-4 border-b border-slate-200 dark:border-slate-800">
      <li v-for="tab in tabs" :key="tab.name">
        <RouterLink
          :to="{ name: tab.name, params: { realm: session.realm } }"
          class="-mb-px block border-b-2 border-transparent px-1 py-2 text-sm"
          exact-active-class="border-brand-700 font-semibold"
        >
          {{ t(tab.label) }}
        </RouterLink>
      </li>
    </ul>
  </nav>
</template>
