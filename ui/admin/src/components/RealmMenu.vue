<script setup lang="ts">
import { PhCaretUpDown } from '@phosphor-icons/vue'
import { PopoverClose, PopoverContent, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { useI18n } from 'vue-i18n'
import { useSession } from '@/stores/session'

// The realm of the session. An administrator works in one realm at a time, so the menu does not
// switch: it leads to the page where another realm is chosen (and signed in to).
const { t } = useI18n()
const session = useSession()
</script>

<template>
  <PopoverRoot>
    <PopoverTrigger
      class="flex shrink-0 items-center gap-1.5 rounded-md bg-brand-50 px-2.5 py-1 text-sm font-semibold text-brand-800 dark:bg-brand-800 dark:text-brand-50"
      data-testid="realm-badge"
    >
      {{ t('realm.current', { name: session.realm }) }}
      <PhCaretUpDown :size="12" aria-hidden="true" />
    </PopoverTrigger>
    <PopoverContent align="start" :side-offset="8" class="z-50 w-64 rounded-md border border-slate-200 bg-white p-1 text-sm shadow-lg dark:border-slate-700 dark:bg-slate-900">
      <p class="px-2.5 py-1.5 text-xs text-slate-600 dark:text-slate-400">{{ t('realm.menuHelp') }}</p>
      <PopoverClose as-child>
        <RouterLink :to="{ name: 'realm' }" class="block rounded-md px-2.5 py-1.5 hover:bg-slate-100 dark:hover:bg-slate-800">{{ t('realm.another') }}</RouterLink>
      </PopoverClose>
    </PopoverContent>
  </PopoverRoot>
</template>
