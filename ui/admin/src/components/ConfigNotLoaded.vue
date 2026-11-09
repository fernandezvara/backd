<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'

// What a dev-mode backd says before its config has loaded (503 config_not_loaded): nothing
// works until then, and its root page has the report.
defineProps<{ message: string }>()
const { t } = useI18n()
</script>

<template>
  <main class="mx-auto mt-16 max-w-xl space-y-4 px-4">
    <h1 class="text-lg font-semibold">{{ t('configNotLoaded.title') }}</h1>
    <AppAlert kind="error" data-testid="config-not-loaded">
      <p>{{ message || t('configNotLoaded.fallback') }}</p>
    </AppAlert>
    <p class="text-sm">
      {{ t('configNotLoaded.waiting') }}
      <a href="/" class="underline">{{ t('configNotLoaded.report') }}</a>
    </p>
  </main>
</template>
