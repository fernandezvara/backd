<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText, formatDate } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// The settings that clients read at runtime (`_realm/config/`): every key with its effective value, who
// may read it, and the history of changes with a rollback. Values are stored by the server, not in files.
defineProps<{ realm: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()
const toasts = useToasts()

interface Change { path: string; old: unknown; new: unknown }
interface Entry { version: string; at: string; actor: string; kind: 'set' | 'rollback'; rolled_back_to?: string; changes: Change[] }
interface Config {
  version: string
  values: Record<string, unknown>
  overrides: Record<string, unknown>
  invalid: { path: string; reason: string }[]
  keys: { path: string; leaf: boolean; description?: string; readable: boolean; public: boolean }[]
  history: Entry[]
  updated_at?: string
  updated_by?: string
}
const cfg = ref<Config>()
const missing = ref(false)
const error = ref('')
const canWrite = computed(() => session.canWrite('config'))

async function load() {
  error.value = ''
  try {
    cfg.value = await raw<Config>('GET', ['app-config'])
    missing.value = false
  } catch (e) {
    const status = (e as { status?: number }).status
    if (status === 404) missing.value = true
    else error.value = errorText(e, t('appConfig.loadFailed'))
  }
}
onMounted(load)

const at = (tree: unknown, path: string): unknown => path.split('.').reduce<unknown>((n, k) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[k] : undefined), tree)
const leaves = computed(() => (cfg.value?.keys ?? []).filter((k) => k.leaf))
const overridden = (path: string) => at(cfg.value?.overrides, path) !== undefined
const show = (v: unknown) => (typeof v === 'string' ? v : JSON.stringify(v))
const invalidFor = (path: string) => cfg.value?.invalid.find((i) => i.path === path)

const etag = () => ({ 'If-Match': `"${cfg.value!.version}"` })
async function apply(call: () => Promise<{ version: string; changed: boolean }>) {
  try {
    const r = await call()
    toasts.push(r.changed ? t('appConfig.saved', { version: r.version }) : t('appConfig.unchanged'))
    await load()
    return ''
  } catch (e) {
    return errorText(e, t('common.unexpected'))
  }
}

// Edit one key
const editing = ref('')
const draft = ref('')
const editError = ref('')
const busy = ref(false)
function edit(path: string) {
  editing.value = path
  draft.value = JSON.stringify(at(cfg.value?.values, path), null, 2) ?? ''
  editError.value = ''
}
async function save() {
  let value: unknown
  try {
    value = JSON.parse(draft.value)
  } catch {
    editError.value = t('appConfig.notJson')
    return
  }
  busy.value = true
  editError.value = await apply(() => raw('PATCH', ['app-config'], { body: { set: { [editing.value]: value } }, headers: etag() }))
  busy.value = false
  if (!editError.value) editing.value = ''
}
async function reset(path: string) {
  const e = await apply(() => raw('PATCH', ['app-config'], { body: { reset: [path] }, headers: etag() }))
  if (e) toasts.push(e)
}

// Roll back
const rolling = ref('')
const rollError = ref('')
async function rollback() {
  busy.value = true
  rollError.value = await apply(() => raw('POST', ['app-config', 'rollback'], { body: { version: rolling.value }, headers: etag() }))
  busy.value = false
  if (!rollError.value) rolling.value = ''
}
</script>

<template>
  <PageHeader :title="t('appConfig.title')" :description="t('appConfig.description')">
    <AppBadge v-if="cfg" data-testid="app-config-version">{{ t('appConfig.version', { version: cfg.version }) }}</AppBadge>
  </PageHeader>
  <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
  <AppAlert v-else-if="missing">{{ t('appConfig.none') }}</AppAlert>
  <template v-else-if="cfg">
    <AppAlert v-if="cfg.invalid.length" kind="error" class="mb-4">
      <p class="font-medium">{{ t('appConfig.invalid') }}</p>
      <ul class="mt-1 list-disc pl-5"><li v-for="i in cfg.invalid" :key="i.path"><span class="font-mono">{{ i.path }}</span>: {{ i.reason }}</li></ul>
    </AppAlert>
    <div class="grid gap-5 lg:grid-cols-3">
      <AppCard flush class="lg:col-span-2">
        <table class="w-full text-left text-[13px]" data-testid="app-config-keys">
          <thead>
            <tr><th class="px-3 py-2 font-medium">{{ t('appConfig.key') }}</th><th class="px-3 py-2 font-medium">{{ t('appConfig.value') }}</th><th class="px-3 py-2 font-medium">{{ t('appConfig.who') }}</th><th class="px-3 py-2" /></tr>
          </thead>
          <tbody>
            <tr v-for="k in leaves" :key="k.path" class="border-t border-slate-100 align-top dark:border-slate-800">
              <td class="px-3 py-2"><span class="font-mono">{{ k.path }}</span><span v-if="k.description" class="block text-xs text-slate-500">{{ k.description }}</span></td>
              <td class="max-w-[18rem] px-3 py-2">
                <span class="font-mono break-all">{{ show(at(cfg.values, k.path)) }}</span>
                <AppBadge v-if="overridden(k.path)" kind="ok" class="ml-1">{{ t('appConfig.changed') }}</AppBadge>
                <span v-if="invalidFor(k.path)" class="block text-xs text-red-700 dark:text-red-400">{{ invalidFor(k.path)?.reason }}</span>
              </td>
              <td class="px-3 py-2"><AppBadge :kind="k.public ? 'warn' : 'neutral'">{{ k.public ? t('appConfig.public') : k.readable ? t('appConfig.readable') : t('appConfig.hidden') }}</AppBadge></td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <template v-if="canWrite">
                  <AppButton variant="secondary" @click="edit(k.path)">{{ t('common.edit') }}</AppButton>
                  <AppButton v-if="overridden(k.path)" variant="secondary" class="ml-1" @click="reset(k.path)">{{ t('appConfig.reset') }}</AppButton>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </AppCard>
      <AppCard flush class="h-fit">
        <h2 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('appConfig.history') }}</h2>
        <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800" data-testid="app-config-history">
          <li v-for="h in cfg.history" :key="h.version" class="px-4 py-2.5">
            <div class="flex items-center gap-2"><b class="font-mono">{{ h.version }}</b><span class="text-xs text-slate-500">{{ formatDate(h.at) }}</span>
              <AppButton v-if="canWrite && h.version !== cfg.version" variant="secondary" class="ml-auto !h-7 !px-2 !text-xs" @click="((rolling = h.version), (rollError = ''))">{{ t('appConfig.rollback') }}</AppButton></div>
            <p class="text-xs text-slate-500">{{ h.actor }} · {{ h.kind === 'rollback' ? t('appConfig.rolledBack', { version: h.rolled_back_to ?? '' }) : t('appConfig.changes', { n: h.changes.length }) }}</p>
            <p v-for="c in h.changes.slice(0, 3)" :key="c.path" class="truncate font-mono text-xs"><span class="text-slate-500">{{ c.path }}</span> {{ show(c.old) }} → {{ show(c.new) }}</p>
          </li>
          <li v-if="!cfg.history.length" class="px-4 py-3 text-slate-500">{{ t('appConfig.noHistory') }}</li>
        </ul>
      </AppCard>
    </div>
  </template>

  <FormDialog :open="!!editing" :title="t('appConfig.editTitle', { key: editing })" :description="t('appConfig.editHelp')" :submit="t('common.save')" :busy="busy" :error="editError" @update:open="(v: boolean) => !v && (editing = '')" @submit="save">
    <textarea v-model="draft" rows="6" class="block w-full rounded-md border border-slate-300 bg-white p-2 font-mono text-[13px] dark:border-slate-700 dark:bg-slate-900" :aria-label="editing" spellcheck="false" />
  </FormDialog>
  <FormDialog :open="!!rolling" :title="t('appConfig.rollbackTitle', { version: rolling })" :description="t('appConfig.rollbackHelp')" :submit="t('appConfig.rollback')" :busy="busy" :error="rollError" @update:open="(v: boolean) => !v && (rolling = '')" @submit="rollback" />
</template>
