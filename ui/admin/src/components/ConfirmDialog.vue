<script setup lang="ts">
// Destructive actions ask for the thing's name to be typed.
import { DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from './AppButton.vue'
import TextField from './TextField.vue'

const props = defineProps<{ title: string; description?: string; expected: string; action: string; busy?: boolean }>()
const emit = defineEmits<{ confirm: [] }>()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()

const typed = ref('')
const ok = computed(() => typed.value === props.expected)
const tried = ref(false)
watch(open, () => {
  typed.value = ''
  tried.value = false
})

function submit() {
  tried.value = true
  if (!ok.value) return
  open.value = false
  emit('confirm')
}
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 bg-black/50" />
      <DialogContent class="fixed top-1/2 left-1/2 w-[min(28rem,92vw)] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-lg bg-white p-5 shadow-xl dark:bg-slate-900">
        <DialogTitle class="text-lg font-semibold">{{ title }}</DialogTitle>
        <DialogDescription v-if="description" class="text-sm text-slate-600 dark:text-slate-400">{{ description }}</DialogDescription>
        <slot />
        <form class="space-y-4" @submit.prevent="submit">
          <TextField v-model="typed" :label="t('confirm.type', { text: expected })" :error="tried && !ok ? t('confirm.mismatch') : undefined" autocomplete="off" />
          <div class="flex justify-end gap-2">
            <AppButton variant="secondary" @click="open = false">{{ t('common.cancel') }}</AppButton>
            <AppButton type="submit" variant="danger" :busy="busy">{{ action }}</AppButton>
          </div>
        </form>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
