<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'

// Sub-navigation of an area: a row of links to routes of the current realm.
defineProps<{ label: string; tabs: { name: string; label: string }[] }>()
const { t } = useI18n()
const session = useSession()
</script>

<template>
  <nav :aria-label="label" class="mb-4">
    <ul class="flex flex-wrap gap-4 border-b border-slate-200 dark:border-slate-800">
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
