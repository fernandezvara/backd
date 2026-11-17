<script setup lang="ts">
import { PhCopy, PhLockSimple } from '@phosphor-icons/vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { highlight } from '@/lib/highlight'
import { useToasts } from '@/stores/toasts'
import AppBadge from './AppBadge.vue'
import AppButton from './AppButton.vue'

// A file's text, read-only, coloured, with line numbers: the same in a function's code and in the
// configuration's files. The notices cover what the server leaves out (binary, cut, no content).
interface Viewable {
  language: string
  content: string | null
  truncated: boolean
  binary: boolean
  restricted?: boolean
}
const props = defineProps<{ name: string; file?: Viewable; hint?: string }>()
const { t } = useI18n()
const toasts = useToasts()

const text = computed(() => (props.file?.content ?? '').replace(/\n$/, ''))
const lines = computed(() => text.value.split('\n'))
const tokens = computed(() => (props.file?.content == null ? [] : highlight(text.value, props.file.language)))

async function copy() {
  if (props.file?.content == null) return
  try {
    await navigator.clipboard.writeText(props.file.content)
    toasts.push(t('fn.code.copied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}
</script>

<template>
  <div class="flex h-full min-h-0 min-w-0 flex-col">
    <div v-if="file" class="flex shrink-0 flex-wrap items-center gap-2 border-b border-slate-200 px-4 py-2 dark:border-slate-800">
      <span class="font-mono text-[13px] font-medium break-all" data-testid="fn-file-name">{{ name }}</span>
      <AppBadge><PhLockSimple :size="11" class="mr-0.5 inline" aria-hidden="true" />{{ t('fn.code.readOnly') }}</AppBadge>
      <span v-if="hint" class="text-xs text-slate-500">{{ hint }}</span>
      <slot name="meta" />
      <div class="ml-auto flex items-center gap-2">
        <slot name="actions" />
        <AppButton v-if="file.content != null" variant="secondary" @click="copy"><PhCopy :size="14" aria-hidden="true" />{{ t('fn.code.copy') }}</AppButton>
      </div>
    </div>
    <p v-if="!file" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.pick') }}</p>
    <p v-else-if="file.restricted" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.restricted') }}</p>
    <p v-else-if="file.binary" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.binary') }}</p>
    <p v-else-if="file.content == null" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.noRoom') }}</p>
    <template v-else>
      <p v-if="file.truncated" class="border-b border-slate-200 bg-amber-50 px-4 py-1.5 text-xs text-amber-900 dark:border-slate-800 dark:bg-amber-950/40 dark:text-amber-200">{{ t('fn.code.truncated') }}</p>
      <p v-if="file.content === ''" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.empty') }}</p>
      <div v-else class="flex min-h-0 flex-1 overflow-auto py-2 font-mono text-[12.5px] leading-relaxed" tabindex="0" data-testid="fn-source">
        <pre class="sticky left-0 shrink-0 select-none bg-white px-3 text-right text-slate-400 dark:bg-slate-900" aria-hidden="true">{{ lines.map((_, i) => i + 1).join('\n') }}</pre>
        <pre class="pr-4"><span v-for="(tk, i) in tokens" :key="i" :class="tk.type ? `hl-${tk.type}` : undefined">{{ tk.text }}</span></pre>
      </div>
    </template>
  </div>
</template>
