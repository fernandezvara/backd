<script setup lang="ts">
import { PhCaretRight, PhCopy, PhFile, PhLockSimple } from '@phosphor-icons/vue'
import type { FunctionSource } from 'backd-js'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useToasts } from '@/stores/toasts'

// A function's code, read-only: the files it has, the shared modules it imports and what it depends on.
// Editing happens in the configuration repository; this page only shows what runs.
const props = defineProps<{ database: string; name: string }>()
const emit = defineEmits<{ loaded: [source: FunctionSource] }>()
const { t } = useI18n()
const admin = useAdmin()
const toasts = useToasts()

const source = ref<FunctionSource>()
const error = ref('')
const selected = ref('')

async function load() {
  error.value = ''
  try {
    source.value = await admin.functionSource(`${props.database}/${props.name}`)
    selected.value = source.value.entry
    emit('loaded', source.value)
  } catch (e) {
    error.value = errorText(e, t('fn.code.loadFailed'))
  }
}
onMounted(load)
watch(() => `${props.database}/${props.name}`, load)

// File names as the function sees them: next to it, or up and over (`../lib/relay.ts`).
const folder = computed(() => (source.value ? source.value.entry.split('/').slice(0, -1).join('/') : ''))
const shortName = (path: string) => (path.startsWith(`${folder.value}/`) ? path.slice(folder.value.length + 1) : `../${path}`)
const groups = computed(() => {
  const files = source.value?.files ?? []
  return [
    { id: 'own', label: `${folder.value}/`, files: files.filter((f) => ['entry', 'config', 'schema', 'code'].includes(f.role)) },
    { id: 'shared', label: t('fn.code.shared'), files: files.filter((f) => f.role === 'shared' || f.role === 'project') },
  ].filter((g) => g.files.length)
})
const file = computed(() => source.value?.files.find((f) => f.path === selected.value))
const lines = computed(() => (file.value?.content ?? '').replace(/\n$/, '').split('\n'))

async function copy() {
  if (file.value?.content == null) return
  try {
    await navigator.clipboard.writeText(file.value.content)
    toasts.push(t('fn.code.copied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}

const kindLabel = (k: string) => t(`fn.deps.kinds.${k}`)
const kindBadge = (k: string) => (k === 'local' ? 'neutral' : k === 'unresolved' ? 'warn' : 'blue')
</script>

<template>
  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <div v-else-if="!source" class="text-sm text-slate-600 dark:text-slate-400" role="status">{{ t('common.loading') }}</div>
  <template v-else>
    <div class="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
      <AppCard flush class="h-fit">
        <nav :aria-label="t('fn.code.files')" class="p-2" data-testid="fn-files">
          <template v-for="g in groups" :key="g.id">
            <p class="px-2 pb-1 pt-1 text-[11px] font-semibold tracking-wider text-slate-500 uppercase">{{ g.label }}</p>
            <button
              v-for="f in g.files"
              :key="f.path"
              type="button"
              class="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left font-mono text-[12px] hover:bg-slate-100 dark:hover:bg-slate-800"
              :class="f.path === selected ? 'bg-slate-100 font-semibold dark:bg-slate-800' : ''"
              :aria-current="f.path === selected ? 'true' : undefined"
              @click="selected = f.path"
            >
              <PhFile :size="14" class="shrink-0 text-slate-400" aria-hidden="true" />
              <span class="truncate">{{ shortName(f.path) }}</span>
            </button>
          </template>
          <p v-if="source.omitted" class="px-2 pt-2 text-xs text-slate-500">{{ t('fn.code.omitted', { n: source.omitted }) }}</p>
        </nav>
      </AppCard>

      <AppCard flush class="min-w-0">
        <div v-if="file" class="flex flex-wrap items-center gap-2 border-b border-slate-200 px-4 py-2 dark:border-slate-800">
          <span class="font-mono text-[13px] font-medium" data-testid="fn-file-name">{{ shortName(file.path) }}</span>
          <AppBadge><PhLockSimple :size="11" class="mr-0.5 inline" aria-hidden="true" />{{ t('fn.code.readOnly') }}</AppBadge>
          <span class="text-xs text-slate-500">{{ t('fn.code.editHint') }}</span>
          <AppButton v-if="file.content != null" variant="secondary" class="ml-auto" @click="copy"><PhCopy :size="14" aria-hidden="true" />{{ t('fn.code.copy') }}</AppButton>
        </div>
        <p v-if="file?.binary" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.binary') }}</p>
        <p v-else-if="file && file.content == null" class="p-4 text-sm text-slate-600 dark:text-slate-400">{{ t('fn.code.noRoom') }}</p>
        <template v-else-if="file">
          <p v-if="file.truncated" class="border-b border-slate-200 bg-amber-50 px-4 py-1.5 text-xs text-amber-900 dark:border-slate-800 dark:bg-amber-950/40 dark:text-amber-200">{{ t('fn.code.truncated') }}</p>
          <div class="flex max-h-[34rem] overflow-auto py-2 font-mono text-[12.5px] leading-relaxed" tabindex="0" data-testid="fn-source">
            <pre class="sticky left-0 shrink-0 select-none bg-white px-3 text-right text-slate-400 dark:bg-slate-900" aria-hidden="true">{{ lines.map((_, i) => i + 1).join('\n') }}</pre>
            <pre class="pr-4">{{ file.content }}</pre>
          </div>
        </template>
      </AppCard>
    </div>

    <section class="mt-5" aria-labelledby="deps-h">
      <AppCard flush>
        <h2 id="deps-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('fn.deps.title') }}</h2>
        <p v-if="!source.dependencies.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('fn.deps.none') }}</p>
        <table v-else class="w-full text-left text-sm" data-testid="fn-deps">
          <thead>
            <tr>
              <th scope="col" class="px-4 py-2">{{ t('fn.deps.specifier') }}</th>
              <th scope="col" class="px-4 py-2">{{ t('fn.deps.kind') }}</th>
              <th scope="col" class="px-4 py-2">{{ t('fn.deps.points') }}</th>
              <th scope="col" class="px-4 py-2">{{ t('fn.deps.usedBy') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in source.dependencies" :key="`${d.kind}|${d.specifier}|${d.resolved}`" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
              <td class="px-4 py-2 font-mono text-xs">{{ d.specifier }}</td>
              <td class="px-4 py-2"><AppBadge :kind="kindBadge(d.kind)">{{ kindLabel(d.kind) }}</AppBadge></td>
              <td class="px-4 py-2 text-slate-600 dark:text-slate-400">
                <button v-if="d.resolved && source.files.some((f) => f.path === d.resolved)" type="button" class="inline-flex items-center gap-1 font-mono text-xs underline" @click="selected = d.resolved!"><PhCaretRight :size="10" aria-hidden="true" />{{ shortName(d.resolved) }}</button>
                <span v-else-if="d.kind === 'local'" class="text-amber-800 dark:text-amber-300">{{ t('fn.deps.missing') }}</span>
                <span v-else-if="d.package" class="font-mono text-xs">{{ d.package }}<template v-if="d.version"> @ {{ d.version }}</template></span>
                <span v-else-if="d.maps_to" class="font-mono text-xs">{{ d.maps_to }}</span>
                <span v-else>—</span>
              </td>
              <td class="px-4 py-2 font-mono text-xs text-slate-600 dark:text-slate-400">{{ d.used_by.map(shortName).join(', ') }}</td>
            </tr>
          </tbody>
        </table>
      </AppCard>
    </section>
  </template>
</template>
