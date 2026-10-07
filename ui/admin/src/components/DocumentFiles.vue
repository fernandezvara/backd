<script setup lang="ts">
import type { Doc } from 'backd-js'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import type { FileFieldConfig } from '@/stores/config'
import { useToasts } from '@/stores/toasts'

// The files of a document's file fields: what each holds, a preview of the
// images, and download, upload and remove, through the admin data route (past
// the collection's rules, audited). Links come with the document
// (`file_links`); a download asks for a fresh one, since they expire.
const props = defineProps<{
  database: string
  collection: string
  doc: Doc
  fields: Record<string, FileFieldConfig>
  writable: boolean
}>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n()
const admin = useAdmin()
const toasts = useToasts()
const col = computed(() => admin.data(props.database, props.collection))

interface Held {
  id: string
  name: string
  size: number
  type: string
  url?: string
}
const held = (field: string): Held[] => {
  const v = (props.doc as Record<string, unknown>)[field]
  return v == null ? [] : ((Array.isArray(v) ? v : [v]) as Held[])
}
const names = computed(() => Object.keys(props.fields).sort())

function size(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`
  return `${(n / 1024 ** 3).toFixed(1)} GiB`
}
function limits(f: FileFieldConfig): string {
  const base = f.multiple ? t('data.doc.files.limit', { n: f.max_files, size: size(f.max_size) }) : t('data.doc.files.limitOne', { size: size(f.max_size) })
  return `${base}. ${f.types.length ? t('data.doc.files.types', { types: f.types.join(', ') }) : t('data.doc.files.anyType')}.`
}
const canUpload = (field: string) => props.writable && (props.fields[field].multiple ? held(field).length < props.fields[field].max_files : true)

// One operation at a time, with the bar for an upload.
const busy = ref(false)
const progress = ref<{ name: string; pct: number } | null>(null)

async function upload(field: string, event: Event) {
  const input = event.target as HTMLInputElement
  const picked = input.files?.[0]
  input.value = ''
  if (!picked) return
  busy.value = true
  progress.value = { name: picked.name, pct: 0 }
  try {
    await col.value.uploadFile(props.doc.id, field, picked, {
      ifMatch: props.doc._meta.version,
      onProgress: ({ loaded, total }) => {
        progress.value = { name: picked.name, pct: total ? Math.round((100 * loaded) / total) : 0 }
      },
    })
    toasts.push(t('data.doc.files.uploaded', { name: picked.name }))
    emit('changed')
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    progress.value = null
    busy.value = false
  }
}

async function download(field: string, f: Held) {
  try {
    await col.value.downloadFile(props.doc.id, field, f.id)
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// The file asked about stays until the answer: the dialog closes before it confirms.
const removing = ref<{ field: string; file: Held } | null>(null)
const removeOpen = ref(false)
function askRemove(field: string, file: Held) {
  removing.value = { field, file }
  removeOpen.value = true
}
async function remove() {
  const target = removing.value
  if (!target) return
  busy.value = true
  try {
    await col.value.deleteFile(props.doc.id, target.field, target.file.id, { ifMatch: props.doc._meta.version })
    toasts.push(t('data.doc.files.removed', { name: target.file.name }))
    emit('changed')
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="mt-8 max-w-3xl border-t border-slate-200 pt-4 dark:border-slate-800" data-testid="files" :aria-label="t('data.doc.files.title')">
    <h2 class="text-lg font-semibold">{{ t('data.doc.files.title') }}</h2>
    <p v-if="!writable" class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('data.doc.files.readonly') }}</p>

    <div v-for="field in names" :key="field" class="mt-4 rounded-md border border-slate-200 p-3 dark:border-slate-700" :data-testid="`files-${field}`">
      <div class="flex flex-wrap items-baseline justify-between gap-2">
        <h3 class="font-mono text-sm font-medium">{{ field }}</h3>
        <span v-if="fields[field].upload === 'direct'" class="text-xs text-slate-600 dark:text-slate-400">{{ t('data.doc.files.direct') }}</span>
      </div>
      <p class="text-xs text-slate-600 dark:text-slate-400">{{ limits(fields[field]) }}</p>

      <p v-if="!held(field).length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('data.doc.files.none') }}</p>
      <ul v-else class="mt-2 space-y-3">
        <li v-for="f in held(field)" :key="f.id" class="flex flex-wrap items-center gap-3" data-testid="file">
          <img v-if="f.url && f.type.startsWith('image/')" :src="f.url" :alt="t('data.doc.files.preview', { name: f.name })" class="h-16 w-16 rounded border border-slate-200 object-cover dark:border-slate-700" />
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-medium" data-testid="file-name">{{ f.name }}</p>
            <p class="text-xs text-slate-600 dark:text-slate-400">{{ f.type }} · {{ size(f.size) }}</p>
          </div>
          <AppButton variant="secondary" :aria-label="t('data.doc.files.downloadLabel', { name: f.name })" @click="download(field, f)">{{ t('data.doc.files.download') }}</AppButton>
          <AppButton v-if="writable" variant="danger" :disabled="busy" :aria-label="t('data.doc.files.removeLabel', { name: f.name })" @click="askRemove(field, f)">{{ t('data.doc.files.remove') }}</AppButton>
        </li>
      </ul>

      <label v-if="canUpload(field)" class="mt-3 block text-sm">
        <span class="mr-2">{{ t('data.doc.files.uploadLabel', { field }) }}</span>
        <input type="file" :disabled="busy" :data-testid="`upload-${field}`" :aria-label="t('data.doc.files.uploadLabel', { field })" @change="upload(field, $event)" />
      </label>
    </div>

    <p v-if="progress" class="mt-3 text-sm" role="status" data-testid="upload-progress">
      {{ t('data.doc.files.uploading', { name: progress.name }) }}
      <progress :value="progress.pct" max="100" class="ml-2 align-middle"></progress> {{ progress.pct }}%
    </p>

    <ConfirmDialog v-model:open="removeOpen" :title="t('data.doc.files.removeTitle', { name: removing?.file.name ?? '' })" :description="t('data.doc.files.removeBody')" :expected="removing?.file.name ?? ''" :action="t('data.doc.files.removeAction')" @confirm="remove" />
  </section>
</template>
