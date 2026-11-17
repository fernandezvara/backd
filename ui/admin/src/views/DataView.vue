<script setup lang="ts">
import { PhDatabase, PhMagnifyingGlass, PhSealCheck, PhTable } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()
const session = useSession()
const router = useRouter()

onMounted(() => cfg.load())

interface Col {
  name: string
  schema?: { properties?: Record<string, unknown> }
  indexes?: unknown[]
  rules?: Record<string, unknown>
  files?: Record<string, unknown>
  policy?: { soft_delete?: unknown; custom_id?: unknown; organization?: unknown }
}
const query = ref('')
const only = ref('')
const databases = computed(() => {
  const dbs = (cfg.config?.databases ?? {}) as Record<string, { collections?: Col[] }>
  const q = query.value.trim().toLowerCase()
  return Object.entries(dbs)
    .filter(([name]) => !only.value || name === only.value)
    .map(([name, db]) => ({ name, collections: (db.collections ?? []).filter((c) => !q || c.name.toLowerCase().includes(q)) }))
    .filter((db) => db.collections.length)
    .sort((a, b) => a.name.localeCompare(b.name))
})
const dbNames = computed(() => Object.keys(cfg.config?.databases ?? {}).sort())
const count = computed(() => databases.value.reduce((n, db) => n + db.collections.length, 0))
const fieldCount = (c: Col) => Object.keys(c.schema?.properties ?? {}).length

// A level without the configuration area can't list collections: it types them.
const knows = computed(() => session.canRead('config'))
const database = ref('')
const collection = ref('')
function open() {
  if (database.value.trim() && collection.value.trim()) {
    void router.push({ name: 'data-collection', params: { realm: props.realm, database: database.value.trim(), collection: collection.value.trim() } })
  }
}
</script>

<template>
  <PageHeader :title="t('data.title')" :description="t('data.help')">
    <RouterLink :to="{ name: 'data-checks', params: { realm } }" class="inline-flex h-9 items-center gap-1.5 rounded-md border border-slate-300 bg-white px-3 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800" data-testid="open-checks"><PhSealCheck aria-hidden="true" /> {{ t('checks.title') }}</RouterLink>
  </PageHeader>

  <template v-if="knows">
    <AppAlert v-if="cfg.error" kind="error" class="mt-4">{{ cfg.error }}</AppAlert>
    <p v-else-if="!cfg.config" class="mt-4 text-sm" role="status">{{ t('common.loading') }}</p>
    <template v-else>
      <div class="mb-4 flex flex-wrap items-center gap-2">
        <div class="relative">
          <PhMagnifyingGlass class="absolute left-2.5 top-2.5 text-slate-400" aria-hidden="true" />
          <input v-model="query" type="search" :placeholder="t('data.filter')" :aria-label="t('data.filter')" class="h-9 w-72 rounded-md border border-slate-300 bg-white pl-8 pr-2 text-sm dark:border-slate-700 dark:bg-slate-900" />
        </div>
        <select v-if="dbNames.length > 1" v-model="only" :aria-label="t('data.allDatabases')" class="h-9 rounded-md border border-slate-300 bg-white px-2 text-sm dark:border-slate-700 dark:bg-slate-900">
          <option value="">{{ t('data.allDatabases') }}</option>
          <option v-for="n in dbNames" :key="n" :value="n">{{ n }}</option>
        </select>
        <span class="ml-auto text-xs text-slate-500">{{ t('data.countLine', { n: count }) }}</span>
      </div>
      <p v-if="!count" class="text-sm text-slate-600 dark:text-slate-400">{{ query || only ? t('data.noMatch') : t('data.noCollections') }}</p>
      <section v-for="db in databases" :key="db.name" class="mb-6" :aria-labelledby="`ddb-${db.name}`">
        <h2 :id="`ddb-${db.name}`" class="mb-2 flex items-center gap-2 text-[13px] font-semibold"><PhDatabase class="text-slate-400" aria-hidden="true" /> {{ db.name }}</h2>
        <ul class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <li v-for="c in db.collections" :key="c.name">
            <RouterLink
              :to="{ name: 'data-collection', params: { realm, database: db.name, collection: c.name } }"
              class="card-soft block rounded-lg border border-slate-200 bg-white p-4 hover:border-brand-600 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-brand-400"
              :data-testid="`open-${db.name}-${c.name}`"
            >
              <span class="flex items-center gap-2">
                <PhTable :size="18" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />
                <span class="font-mono font-semibold">{{ c.name }}</span>
                <span class="ml-auto text-xs text-slate-500">{{ t('data.fieldsCount', { n: fieldCount(c) }) }}</span>
              </span>
              <span class="mt-3 flex min-h-5 flex-wrap gap-1.5">
                <AppBadge v-if="c.rules && Object.keys(c.rules).length">{{ t('data.tagRules') }}</AppBadge>
                <AppBadge v-if="c.policy?.soft_delete" kind="warn">{{ t('data.tagTrash') }}</AppBadge>
                <AppBadge v-if="c.policy?.organization" kind="violet">{{ t('data.tagOrg') }}</AppBadge>
                <AppBadge v-if="c.policy?.custom_id" kind="blue">{{ t('data.tagIds') }}</AppBadge>
                <AppBadge v-if="c.files && Object.keys(c.files).length" kind="blue">{{ t('data.tagFiles') }}</AppBadge>
                <AppBadge v-if="c.indexes?.length">{{ t('data.tagIndexes', { n: c.indexes.length }) }}</AppBadge>
              </span>
            </RouterLink>
          </li>
        </ul>
      </section>
    </template>
  </template>
  <form v-else class="mt-4 max-w-md space-y-4" @submit.prevent="open">
    <AppAlert>{{ t('data.collectionsNeedConfig') }}</AppAlert>
    <TextField v-model="database" :label="t('data.databaseLabel')" autocomplete="off" required />
    <TextField v-model="collection" :label="t('data.collectionLabel')" autocomplete="off" required />
    <AppButton type="submit">{{ t('data.open') }}</AppButton>
  </form>
</template>
