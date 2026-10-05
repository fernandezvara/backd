<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'

defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()

// Only what whoami says: the server enforces it, the interface reflects it.
const readable = computed(() => session.access?.read ?? [])
const writable = computed(() => session.access?.write ?? [])
const who = computed(() => session.email || t('home.key', { name: session.access?.key ?? '' }))
const areaName = (a: string) => t(`areas.${a}`)
</script>

<template>
  <h1 class="text-2xl font-semibold">{{ t('home.title') }}</h1>
  <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('home.signedInAs', { who }) }}</p>
  <dl class="mt-6 grid gap-4 sm:grid-cols-3">
    <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.level') }}</dt>
      <dd class="mt-1 font-medium" data-testid="level">{{ t(`home.levels.${session.access?.level ?? 'read'}`) }}</dd>
    </div>
    <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.canRead') }}</dt>
      <dd class="mt-1" data-testid="can-read">{{ readable.length ? readable.map(areaName).join(', ') : t('home.nothing') }}</dd>
    </div>
    <div class="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('home.canWrite') }}</dt>
      <dd class="mt-1" data-testid="can-write">{{ writable.length ? writable.map(areaName).join(', ') : t('home.nothing') }}</dd>
    </div>
  </dl>
</template>
