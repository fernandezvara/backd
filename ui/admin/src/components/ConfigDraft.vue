<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import { downloadText, draftProblem } from '@/lib/draft'

// A configuration file to edit in the browser and download, for review and commit.
// The server is never written to: backd reads its configuration at startup.
const props = defineProps<{ path: string; initial: string; kind: 'json' | 'yaml'; label: string }>()
const { t } = useI18n()

const open = ref(false)
const text = ref(props.initial)
watch(() => props.initial, (v) => (text.value = v))
const changed = computed(() => text.value !== props.initial)
const problem = computed(() => draftProblem(props.kind, text.value))
const filename = computed(() => props.path.split('/').pop() ?? props.path)

function download() {
  downloadText(filename.value, text.value.endsWith('\n') ? text.value : text.value + '\n', props.kind === 'json' ? 'application/json' : 'text/yaml')
}
</script>

<template>
  <div class="mt-2">
    <AppButton variant="secondary" :aria-expanded="open" @click="open = !open">{{ open ? t('config.draft.hide') : t('config.draft.edit', { file: filename }) }}</AppButton>
    <div v-if="open" class="mt-2 space-y-2" :data-testid="`draft-${filename}`">
      <p class="text-xs text-slate-600 dark:text-slate-400">{{ t('config.draft.note', { path }) }}</p>
      <textarea v-model="text" :aria-label="label" spellcheck="false" rows="14" class="w-full rounded border border-slate-300 bg-white p-2 font-mono text-xs dark:border-slate-600 dark:bg-slate-900" :aria-invalid="!!problem"></textarea>
      <p v-if="problem" class="text-sm text-red-700 dark:text-red-400" role="alert">{{ t('config.draft.invalid', { problem }) }}</p>
      <p v-else-if="changed" class="text-sm text-amber-800 dark:text-amber-300" role="status">{{ t('config.draft.changed') }}</p>
      <div class="flex flex-wrap gap-2">
        <AppButton :disabled="!!problem" @click="download">{{ t('config.draft.download', { file: filename }) }}</AppButton>
        <AppButton variant="secondary" :disabled="!changed" @click="text = initial">{{ t('config.draft.reset') }}</AppButton>
      </div>
    </div>
  </div>
</template>
