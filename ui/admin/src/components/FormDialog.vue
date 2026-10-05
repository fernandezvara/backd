<script setup lang="ts">
import { DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { useI18n } from 'vue-i18n'
import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'

defineProps<{ title: string; description?: string; submit: string; busy?: boolean; error?: string }>()
defineEmits<{ submit: [] }>()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 bg-black/50" />
      <DialogContent class="fixed top-1/2 left-1/2 max-h-[90vh] w-[min(30rem,92vw)] -translate-x-1/2 -translate-y-1/2 space-y-4 overflow-y-auto rounded-lg bg-white p-5 shadow-xl dark:bg-slate-900">
        <DialogTitle class="text-lg font-semibold">{{ title }}</DialogTitle>
        <DialogDescription v-if="description" class="text-sm text-slate-600 dark:text-slate-400">{{ description }}</DialogDescription>
        <form class="space-y-4" @submit.prevent="$emit('submit')">
          <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
          <slot />
          <div class="flex justify-end gap-2">
            <AppButton variant="secondary" @click="open = false">{{ t('common.cancel') }}</AppButton>
            <AppButton type="submit" :busy="busy">{{ submit }}</AppButton>
          </div>
        </form>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
