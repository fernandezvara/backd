<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>

<template>
  <main class="mx-auto max-w-sm space-y-4 px-4 py-16">
    <h1 class="text-xl font-semibold">{{ t('notFound.title') }}</h1>
    <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('notFound.body') }}</p>
    <RouterLink :to="{ name: 'realm' }" class="text-sm underline">{{ t('notFound.home') }}</RouterLink>
  </main>
</template>
