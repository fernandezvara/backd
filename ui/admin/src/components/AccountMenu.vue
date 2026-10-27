<script setup lang="ts">
import { PopoverContent, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'
import AccountControls from './AccountControls.vue'

const { t } = useI18n()
const session = useSession()
const initial = computed(() => (session.email || '?').charAt(0).toUpperCase())
</script>

<template>
  <PopoverRoot>
    <PopoverTrigger
      class="grid size-8 place-items-center rounded-full bg-brand-700 text-sm font-semibold text-white hover:bg-brand-800"
      :aria-label="t('account.label')"
      data-testid="account-menu"
    >
      <span aria-hidden="true">{{ initial }}</span>
    </PopoverTrigger>
    <PopoverContent align="end" :side-offset="8" class="z-50 w-64 rounded-md border border-slate-200 bg-white p-4 shadow-lg dark:border-slate-700 dark:bg-slate-900" :aria-label="t('account.label')">
      <AccountControls />
    </PopoverContent>
  </PopoverRoot>
</template>
