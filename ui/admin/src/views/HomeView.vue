<script setup lang="ts">
import AppCard from '@/components/AppCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'

defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()

// Only what whoami says: the server enforces it, the interface reflects it.
const readable = computed(() => session.readable)
const writable = computed(() => session.writable)
const who = computed(() => session.email || t('home.key', { name: session.access?.key ?? '' }))
const areaName = (a: string) => t(`areas.${a}`)
</script>

<template>
  <PageHeader :title="t('home.title')" :description="t('home.signedInAs', { who })" />
  <dl class="grid gap-4 sm:grid-cols-3">
    <AppCard>
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.level') }}</dt>
      <dd class="mt-1 font-medium" data-testid="level">{{ t(`home.levels.${session.access?.level ?? 'read'}`) }}</dd>
    </AppCard>
    <AppCard>
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.canRead') }}</dt>
      <dd class="mt-1" data-testid="can-read">{{ readable.length ? readable.map(areaName).join(', ') : t('home.nothing') }}</dd>
    </AppCard>
    <AppCard>
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.canWrite') }}</dt>
      <dd class="mt-1" data-testid="can-write">{{ writable.length ? writable.map(areaName).join(', ') : t('home.nothing') }}</dd>
    </AppCard>
  </dl>
</template>
