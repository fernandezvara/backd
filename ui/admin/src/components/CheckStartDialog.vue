<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import FormDialog from '@/components/FormDialog.vue'

// The confirmation before a schema check: it reads every document, so it says so.
defineProps<{ scope: string; estimated?: number; busy?: boolean }>()
defineEmits<{ submit: [] }>()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
</script>

<template>
  <FormDialog v-model:open="open" :title="t('checks.start.title')" :description="t('checks.start.body')" :submit="t('checks.start.action')" :busy="busy" @submit="$emit('submit')">
    <p class="font-mono text-sm" data-testid="check-scope">{{ scope }}</p>
    <p v-if="estimated !== undefined" class="text-sm" data-testid="check-estimate">{{ t('checks.start.estimated', { n: estimated }) }}</p>
    <p class="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100" data-testid="check-warning">{{ t('checks.start.warning') }}</p>
  </FormDialog>
</template>
