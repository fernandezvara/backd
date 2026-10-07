<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import { downloadText, draftProblem } from '@/lib/draft'
import { useConfigDrafts } from '@/stores/configDrafts'

// A configuration file to edit in the browser and download, for review and commit.
// The server is never written to: backd reads its configuration at startup.
const props = defineProps<{ path: string; initial: string; kind: 'json' | 'yaml'; label: string }>()
const { t } = useI18n()

const drafts = useConfigDrafts()
const open = ref(false)
const text = ref(props.initial)
watch(() => props.initial, (v) => (text.value = v))
const changed = computed(() => text.value !== props.initial)
const problem = computed(() => draftProblem(props.kind, text.value))
const filename = computed(() => props.path.split('/').pop() ?? props.path)
// The server checks the draft against the whole realm; its paths are relative to the realm.
const inRealm = computed(() => props.path.split('/').slice(1).join('/'))
watch([text, problem], () => {
  if (problem.value) drafts.set(inRealm.value, props.initial, props.initial) // not sent while it isn't valid
  else drafts.set(inRealm.value, text.value, props.initial)
})
onBeforeUnmount(() => drafts.set(inRealm.value, props.initial, props.initial))
// What the server found, for all the drafts together.
const verdict = computed(() => (changed.value && !problem.value ? drafts.result : null))
const blocked = computed(() => !!problem.value || (changed.value && (drafts.checking || verdict.value?.ok === false)))

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
      <template v-else-if="changed">
        <p v-if="drafts.checking" class="text-sm text-slate-600 dark:text-slate-400" role="status" data-testid="draft-checking">{{ t('config.draft.checking') }}</p>
        <div v-else-if="verdict?.ok" class="text-sm text-green-800 dark:text-green-300" role="status" data-testid="draft-ok">
          {{ t('config.draft.ok', { n: verdict.checked, changed: verdict.changed.length }) }}
        </div>
        <div v-else-if="verdict" class="text-sm text-red-700 dark:text-red-400" role="alert" data-testid="draft-problems">
          <p>{{ t('config.draft.notOk', { n: verdict.checked }) }}</p>
          <ul class="mt-1 list-disc pl-5">
            <li v-for="p in verdict.problems" :key="p.message" class="font-mono text-xs break-words">{{ p.message }}</li>
          </ul>
        </div>
        <p v-else-if="drafts.unavailable" class="text-sm text-amber-800 dark:text-amber-300" role="status">{{ t('config.draft.unchecked', { why: drafts.unavailable }) }}</p>
        <p v-else class="text-sm text-amber-800 dark:text-amber-300" role="status">{{ t('config.draft.changed') }}</p>
      </template>
      <div class="flex flex-wrap gap-2">
        <AppButton :disabled="blocked" @click="download">{{ t('config.draft.download', { file: filename }) }}</AppButton>
        <AppButton variant="secondary" :disabled="!changed" @click="text = initial">{{ t('config.draft.reset') }}</AppButton>
      </div>
    </div>
  </div>
</template>
