<script setup lang="ts">
import { PhCaretRight } from '@phosphor-icons/vue'
import { useI18n } from 'vue-i18n'
import type { Crumb } from '@/lib/crumbs'

// Where you are, from the realm to the page; every part with a page of its own leads there.
defineProps<{ crumbs: Crumb[] }>()
const { t } = useI18n()
</script>

<template>
  <nav :aria-label="t('breadcrumb.label')" class="mx-auto max-w-6xl px-4 pt-3 text-sm text-slate-600 dark:text-slate-400">
    <ol class="flex flex-wrap items-center gap-x-1.5 gap-y-1">
      <li v-for="(c, i) in crumbs" :key="i" class="flex items-center gap-1.5">
        <!-- The first part is the realm: in the accent colour, with its name said. -->
        <RouterLink v-if="c.to && i === 0" :to="c.to" class="font-semibold text-brand-700 hover:underline dark:text-brand-400" data-testid="realm-crumb">{{ t('realm.label') }}: {{ c.label }}</RouterLink>
        <RouterLink v-else-if="c.to" :to="c.to" class="hover:text-slate-900 hover:underline dark:hover:text-slate-100">{{ c.label }}</RouterLink>
        <span v-else :aria-current="i === crumbs.length - 1 ? 'page' : undefined" :class="i === crumbs.length - 1 ? 'font-semibold text-slate-900 dark:text-slate-100' : ''">{{ c.label }}</span>
        <PhCaretRight v-if="i < crumbs.length - 1" :size="12" aria-hidden="true" />
      </li>
    </ol>
  </nav>
</template>
