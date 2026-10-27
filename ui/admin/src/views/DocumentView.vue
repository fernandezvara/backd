<script setup lang="ts">
import { ValidationError, VersionMismatchError, type AdminOrg, type Doc } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import DocumentFiles from '@/components/DocumentFiles.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormDialog from '@/components/FormDialog.vue'
import JsonField from '@/components/JsonField.vue'
import JsonView from '@/components/JsonView.vue'
import SchemaForm from '@/components/SchemaForm.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate } from '@/lib/format'
import { errorsByPath, formable, newDocument, withoutProperties, type Schema } from '@/lib/schema'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

const props = defineProps<{ realm: string; database: string; collection: string; id?: string }>()
const { t } = useI18n()
const admin = useAdmin()
const cfg = useRealmConfig()
const session = useSession()
const router = useRouter()
const toasts = useToasts()

const isNew = computed(() => props.id === undefined)
const col = computed(() => admin.data(props.database, props.collection))
const info = computed(() => cfg.collectionInfo(props.database, props.collection))
// The file fields have their own controls (below the form): the form and the
// JSON tab leave them out, and a save never sends them (a PUT keeps the files).
const fileFields = computed(() => info.value?.files ?? {})
const fileNames = computed(() => Object.keys(fileFields.value))
const schema = computed(() => (info.value?.schema ? withoutProperties(info.value.schema as Schema, fileNames.value) : undefined))
const softDelete = computed(() => info.value?.softDelete ?? false)
const canForm = computed(() => formable(schema.value))

// Collections in organizations: a new document names its organization, and an
// administrator may move one to another (`_meta.org` never changes in an update).
const orgMode = computed(() => info.value?.organization ?? null)
const orgChoices = ref<AdminOrg[]>([])
const newOrg = ref('')
async function loadOrgs() {
  try {
    orgChoices.value = await admin.orgs.list({ status: 'active' })
  } catch (e) {
    toasts.push(errorText(e, t('orgs.loadFailed')), 'error')
  }
}
const orgName = (id?: string | null) => (id ? (orgChoices.value.find((o) => o.id === id)?.name ?? id) : t('data.doc.noOrg'))

type Fields = Record<string, unknown>
const doc = ref<Doc>()
const draft = ref<Fields>({})
const original = ref('')
const baseVersion = ref<number | undefined>()
const loadError = ref('')
const tab = ref<'form' | 'json'>('form')
const busy = ref(false)

const split = (d: Doc): Fields => {
  const { id: _id, _meta: _m, ...rest } = d as Record<string, unknown>
  void _id
  void _m
  for (const name of fileNames.value) delete rest[name]
  return structuredClone(rest)
}
function adopt(d: Doc) {
  doc.value = d
  draft.value = split(d)
  original.value = JSON.stringify(draft.value)
  baseVersion.value = d._meta.version
}

const inTrash = computed(() => !!doc.value?._meta.deleted_at)
const canWrite = computed(() => session.canWrite('data'))
const readonly = computed(() => !canWrite.value || inTrash.value)
const dirty = computed(() => JSON.stringify(draft.value) !== original.value)

// A soft-deleted document is only found with `deleted: include`, which a
// collection without a trash refuses: ask for it only where it can exist.
// Where the configuration isn't readable, try, and fall back.
async function fetchDoc(): Promise<Doc> {
  // Links to the files (for the previews) come with the document.
  const fileLinks = fileNames.value.length > 0
  if (info.value && !softDelete.value) return col.value.get(props.id!, { fileLinks })
  try {
    return await col.value.get(props.id!, { deleted: 'include', fileLinks })
  } catch (e) {
    if (info.value || !(e instanceof ValidationError)) throw e
    return col.value.get(props.id!)
  }
}

// A file was uploaded or removed: the document has a new version, and new links.
// Unsaved edits stay; only what the files changed is taken.
async function filesChanged() {
  try {
    const fresh = await fetchDoc()
    if (dirty.value) {
      doc.value = fresh
      baseVersion.value = fresh._meta.version
    } else {
      adopt(fresh)
    }
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

onMounted(async () => {
  await cfg.load()
  tab.value = canForm.value ? 'form' : 'json'
  if (orgMode.value) await loadOrgs()
  if (isNew.value) {
    draft.value = schema.value && canForm.value ? newDocument(schema.value) : {}
    original.value = JSON.stringify(draft.value)
    return
  }
  try {
    adopt(await fetchDoc())
  } catch (e) {
    loadError.value = errorText(e, t('data.doc.loadFailed'))
  }
})

// Errors from the server, by field.
const errors = ref<Record<string, string>>({})
const problems = ref<{ path: string; reason: string }[]>([])
// A newer version exists: show it and let the administrator choose.
const conflict = ref<Doc>()
const showNewer = ref(false)

async function save() {
  busy.value = true
  errors.value = {}
  problems.value = []
  try {
    if (isNew.value) {
      const made = await col.value.create(draft.value, orgMode.value && newOrg.value ? { org: newOrg.value } : undefined)
      toasts.push(t('data.doc.created_ok'))
      await router.replace({ name: 'data-doc', params: { realm: props.realm, database: props.database, collection: props.collection, id: made.id } })
      return
    }
    const saved = await col.value.replace(props.id!, draft.value, { ifMatch: baseVersion.value })
    adopt(saved)
    toasts.push(t('data.doc.saved', { n: saved._meta.version ?? '' }))
  } catch (e) {
    if (e instanceof ValidationError) {
      problems.value = e.details
      errors.value = errorsByPath(e.details)
      if (!e.details.length) toasts.push(e.message, 'error')
    } else if (e instanceof VersionMismatchError) {
      try {
        conflict.value = await fetchDoc()
      } catch (e2) {
        toasts.push(errorText(e2, t('common.unexpected')), 'error')
      }
    } else {
      toasts.push(errorText(e, t('common.unexpected')), 'error')
    }
  } finally {
    busy.value = false
  }
}
function loadNewer() {
  adopt(conflict.value!)
  conflict.value = undefined
  showNewer.value = false
  errors.value = {}
  problems.value = []
}
function keepMine() {
  baseVersion.value = conflict.value!._meta.version
  toasts.push(t('data.doc.conflict.kept', { n: baseVersion.value ?? '' }))
  conflict.value = undefined
  showNewer.value = false
}

// Delete, restore, purge: the document's id is typed.
const deleting = ref(false)
const purging = ref(false)
async function remove(purge: boolean) {
  try {
    await col.value.delete(props.id!, { purge })
    toasts.push(purge ? t('data.doc.purge.done') : softDelete.value ? t('data.doc.delete.trashed') : t('data.doc.delete.done'))
    await router.push({ name: 'data-collection', params: { realm: props.realm, database: props.database, collection: props.collection } })
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}
async function restore() {
  try {
    adopt(await col.value.restore(props.id!))
    toasts.push(t('data.doc.restore.done'))
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// Move to another organization.
const moving = ref(false)
const moveTo = ref('')
const moveBusy = ref(false)
const moveError = ref('')
function openMove() {
  moveTo.value = (doc.value?._meta as { org?: string } | undefined)?.org ?? ''
  moveError.value = ''
  moving.value = true
}
async function move() {
  moveBusy.value = true
  moveError.value = ''
  try {
    const moved = await admin.orgs.transfer(props.database, props.collection, props.id!, moveTo.value || null, { ifMatch: String(baseVersion.value ?? '') || undefined })
    moving.value = false
    toasts.push(t('data.doc.move.done'))
    adopt(moved as Doc)
  } catch (e) {
    moveError.value = errorText(e, t('common.unexpected'))
  } finally {
    moveBusy.value = false
  }
}

const meta = computed(() => doc.value?._meta)
const docOrg = computed(() => (meta.value as { org?: string } | undefined)?.org)
const actor = (v?: string | null) => v || '—'
</script>

<template>
  <h1 class="mt-2 text-2xl font-semibold" data-testid="doc-title">{{ isNew ? t('data.doc.new', { collection }) : t('data.doc.edit', { id: id ?? '' }) }}</h1>

  <AppAlert v-if="loadError" kind="error" class="mt-4">{{ loadError }}</AppAlert>

  <template v-else-if="isNew || doc">
    <AppAlert v-if="inTrash" class="mt-4">{{ t('data.doc.inTrash', { date: formatDate(meta?.purge_at) }) }}</AppAlert>
    <AppAlert v-else-if="!canWrite" class="mt-4">{{ t('data.doc.readonly') }}</AppAlert>

    <dl v-if="meta" class="mt-4 grid max-w-3xl grid-cols-[9rem_1fr] gap-x-4 gap-y-1 text-sm" data-testid="doc-meta" :aria-label="t('data.doc.meta')">
      <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.id') }}</dt>
      <dd class="font-mono text-xs">{{ doc!.id }}</dd>
      <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.version') }}</dt>
      <dd data-testid="doc-version">{{ meta.version ?? '—' }}</dd>
      <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.created') }}</dt>
      <dd>{{ formatDate(meta.created_at) }} · <span class="font-mono text-xs">{{ actor(meta.created_by) }}</span></dd>
      <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.updated') }}</dt>
      <dd>{{ formatDate(meta.updated_at) }} · <span class="font-mono text-xs">{{ actor(meta.updated_by) }}</span></dd>
      <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.owner') }}</dt>
      <dd class="font-mono text-xs">{{ meta.owner ?? t('data.doc.noOwner') }}</dd>
      <template v-if="orgMode">
        <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.org') }}</dt>
        <dd data-testid="doc-org">{{ orgName(docOrg) }}</dd>
      </template>
      <template v-if="meta.deleted_at">
        <dt class="text-slate-600 dark:text-slate-400">{{ t('data.doc.deletedAt') }}</dt>
        <dd>{{ formatDate(meta.deleted_at) }} · <span class="font-mono text-xs">{{ actor(meta.deleted_by) }}</span></dd>
      </template>
    </dl>

    <AppAlert v-if="conflict" kind="error" class="mt-4" data-testid="conflict">
      <p class="font-medium">{{ t('data.doc.conflict.title') }}</p>
      <p class="mt-1">{{ t('data.doc.conflict.body', { n: conflict._meta.version ?? '', mine: baseVersion ?? '' }) }}</p>
      <div class="mt-2 flex flex-wrap gap-2">
        <AppButton variant="secondary" @click="showNewer = !showNewer">{{ showNewer ? t('data.doc.conflict.hide') : t('data.doc.conflict.show') }}</AppButton>
        <AppButton variant="secondary" @click="loadNewer">{{ t('data.doc.conflict.load') }}</AppButton>
        <AppButton @click="keepMine">{{ t('data.doc.conflict.keep', { n: conflict._meta.version ?? '' }) }}</AppButton>
      </div>
      <div v-if="showNewer" class="mt-2"><JsonView :value="conflict" :label="t('data.doc.conflict.show')" /></div>
    </AppAlert>

    <AppAlert v-if="problems.length" kind="error" class="mt-4" data-testid="problems">
      <p class="font-medium">{{ t('data.doc.problems') }}</p>
      <ul class="mt-1 list-disc pl-5">
        <li v-for="p in problems" :key="p.path + p.reason">{{ t('data.doc.problem', { path: p.path || '(document)', reason: p.reason }) }}</li>
      </ul>
    </AppAlert>

    <div class="mt-6 flex flex-wrap items-center justify-between gap-3">
      <div v-if="canForm" class="flex overflow-hidden rounded-md border border-slate-300 text-sm dark:border-slate-600" role="group" :aria-label="t('data.doc.tabs.label')">
        <button type="button" class="px-3 py-1.5" :class="tab === 'form' ? 'bg-brand-700 text-white' : ''" :aria-pressed="tab === 'form'" @click="tab = 'form'">{{ t('data.doc.tabs.form') }}</button>
        <button type="button" class="px-3 py-1.5" :class="tab === 'json' ? 'bg-brand-700 text-white' : ''" :aria-pressed="tab === 'json'" @click="tab = 'json'">{{ t('data.doc.tabs.json') }}</button>
      </div>
      <p v-else class="text-sm text-slate-600 dark:text-slate-400">{{ schema ? t('data.doc.noForm') : t('data.doc.noSchema') }}</p>
      <p v-if="dirty && !readonly" class="text-sm text-amber-800 dark:text-amber-300" role="status">{{ t('data.doc.unsaved') }}</p>
    </div>

    <div v-if="isNew && orgMode" class="mt-4 max-w-md space-y-1">
      <label for="doc-org-new" class="block text-sm font-medium">{{ t('data.doc.org') }}</label>
      <select id="doc-org-new" v-model="newOrg" :required="orgMode === 'required'" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
        <option value="" :disabled="orgMode === 'required'">{{ orgMode === 'required' ? '—' : t('data.doc.noOrg') }}</option>
        <option v-for="o in orgChoices" :key="o.id" :value="o.id">{{ o.name }}</option>
      </select>
      <p class="text-xs text-slate-600 dark:text-slate-400">{{ t('data.doc.orgHelp') }}</p>
    </div>

    <form class="mt-4 max-w-3xl space-y-4" novalidate @submit.prevent="save">
      <SchemaForm v-if="tab === 'form' && canForm && schema" v-model="draft" :schema="schema" :readonly="readonly" :errors="errors" />
      <JsonField v-else id="doc-json" v-model="draft" :label="t('data.doc.tabs.json')" :readonly="readonly" :rows="16" :error="errors['']" />

      <div v-if="!readonly" class="flex flex-wrap gap-2">
        <AppButton type="submit" :busy="busy">{{ isNew ? t('data.doc.create') : t('data.doc.save') }}</AppButton>
      </div>
    </form>

    <DocumentFiles v-if="!isNew && doc && fileNames.length" :database="database" :collection="collection" :doc="doc" :fields="fileFields" :writable="canWrite && !inTrash" @changed="filesChanged" />

    <div v-if="!isNew && canWrite" class="mt-8 flex flex-wrap gap-2 border-t border-slate-200 pt-4 dark:border-slate-800">
      <template v-if="inTrash">
        <AppButton variant="secondary" @click="restore">{{ t('data.doc.restore.open') }}</AppButton>
        <AppButton variant="danger" @click="purging = true">{{ t('data.doc.purge.open') }}</AppButton>
      </template>
      <AppButton v-if="orgMode && !inTrash" variant="secondary" @click="openMove">{{ t('data.doc.move.open') }}</AppButton>
      <AppButton v-if="!inTrash" variant="danger" @click="deleting = true">{{ softDelete ? t('data.doc.delete.open') : t('data.doc.delete.openHard') }}</AppButton>
    </div>

    <FormDialog v-model:open="moving" :title="t('data.doc.move.title')" :description="t('data.doc.move.help')" :submit="t('data.doc.move.action')" :busy="moveBusy" :error="moveError" @submit="move">
      <div class="space-y-1">
        <label for="doc-move-to" class="block text-sm font-medium">{{ t('data.doc.org') }}</label>
        <select id="doc-move-to" v-model="moveTo" class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900">
          <option v-if="orgMode === 'optional'" value="">{{ t('data.doc.noOrg') }}</option>
          <option v-for="o in orgChoices" :key="o.id" :value="o.id">{{ o.name }}</option>
        </select>
      </div>
    </FormDialog>

    <ConfirmDialog v-model:open="deleting" :title="t('data.doc.delete.title', { id: id ?? '' })" :description="softDelete ? t('data.doc.delete.bodySoft') : t('data.doc.delete.body')" :expected="id ?? ''" :action="t('data.doc.delete.action')" @confirm="remove(false)" />
    <ConfirmDialog v-model:open="purging" :title="t('data.doc.purge.title', { id: id ?? '' })" :description="t('data.doc.purge.body')" :expected="id ?? ''" :action="t('data.doc.purge.action')" @confirm="remove(true)" />
  </template>
</template>
