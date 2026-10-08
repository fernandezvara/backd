<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ConfigFrame from '@/components/ConfigFrame.vue'
import ConfigDraft from '@/components/ConfigDraft.vue'
import JsonView from '@/components/JsonView.vue'
import { collectionFileText, schemaFileText } from '@/lib/draft'
import { prettyDuration } from '@/lib/format'
import { useRealmConfig } from '@/stores/config'

defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()

interface CollectionConfig {
  name: string
  schema: unknown
  schema_file: string
  indexes: { fields: string; unique: boolean; ttl: boolean }[]
  indexes_file: string
  rules?: Record<string, { expression: string; from: string }>
  collection_file?: string
  collection_yaml?: string
  policy?: {
    soft_delete?: { retention: string }
    on_owner_delete?: { action: string; remove?: string[] | null; replace?: Record<string, unknown> | null; pull?: Record<string, unknown> | null; unset?: Record<string, unknown> | null }
  }
  policy_file?: string
  files?: Record<string, unknown>
}

const databases = computed(() => {
  const dbs = (cfg.config?.databases ?? {}) as Record<string, { collections?: CollectionConfig[] }>
  return Object.entries(dbs)
    .map(([name, db]) => ({ name, collections: db.collections ?? [] }))
    .filter((db) => db.collections.length)
    .sort((a, b) => a.name.localeCompare(b.name))
})
const OPS = ['read', 'create', 'update', 'delete', 'restore', 'purge']
const hasValues = (o?: Record<string, unknown> | string[] | null) => !!o && (Array.isArray(o) ? o.length > 0 : Object.keys(o).length > 0)
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.collections')">
    <p v-if="!databases.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.collections.none') }}</p>
    <section v-for="db in databases" :key="db.name" class="mb-8" :aria-labelledby="`cdb-${db.name}`">
      <h2 :id="`cdb-${db.name}`" class="text-lg font-semibold">{{ t('config.collections.database', { name: db.name }) }}</h2>
      <details v-for="c in db.collections" :key="c.name" class="mt-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" :data-testid="`col-${db.name}-${c.name}`">
        <summary class="cursor-pointer font-mono text-sm font-semibold">{{ c.name }}</summary>

        <h3 class="mt-4 text-sm font-semibold">{{ t('config.collections.schema') }}</h3>
        <p class="mb-1 font-mono text-xs text-slate-600 dark:text-slate-400">{{ c.schema_file }}</p>
        <JsonView :value="c.schema" :label="`${c.name} ${t('config.collections.schema')}`" />
        <ConfigDraft :path="c.schema_file" :initial="schemaFileText(c.schema, Object.keys(c.files ?? {}))" kind="json" :label="`${c.name} schema.json`" />

        <h3 class="mt-4 text-sm font-semibold">{{ t('config.collections.indexes') }}</h3>
        <p class="mb-1 font-mono text-xs text-slate-600 dark:text-slate-400">{{ c.indexes_file }}</p>
        <p v-if="!c.indexes.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.collections.noIndexes') }}</p>
        <ul v-else class="list-disc pl-5 text-sm">
          <li v-for="ix in c.indexes" :key="ix.fields">
            <code class="font-mono text-xs">{{ ix.fields }}</code>
            <span v-if="ix.unique"> · {{ t('config.collections.unique') }}</span>
            <span v-if="ix.ttl"> · {{ t('config.collections.ttl') }}</span>
          </li>
        </ul>

        <h3 class="mt-4 text-sm font-semibold">{{ t('config.collections.rules') }}</h3>
        <p v-if="c.collection_file" class="mb-1 font-mono text-xs text-slate-600 dark:text-slate-400">{{ c.collection_file }} (rules:)</p>
        <p v-if="!c.rules" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.collections.noRules') }}</p>
        <dl v-else class="space-y-2 text-sm">
          <template v-for="op in OPS" :key="op">
            <div v-if="c.rules[op]">
              <dt class="font-medium">{{ op }} <span class="font-normal text-slate-600 dark:text-slate-400">({{ t('config.collections.from', { key: c.rules[op].from }) }})</span></dt>
              <dd><pre class="overflow-x-auto rounded bg-slate-100 p-2 font-mono text-xs whitespace-pre-wrap dark:bg-slate-800" tabindex="0">{{ c.rules[op].expression.trim() }}</pre></dd>
            </div>
          </template>
        </dl>

        <h3 class="mt-4 text-sm font-semibold">{{ t('config.collections.policy') }}</h3>
        <p v-if="c.policy_file" class="mb-1 font-mono text-xs text-slate-600 dark:text-slate-400">{{ c.policy_file }}</p>
        <p v-if="!c.policy" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.collections.noPolicy') }}</p>
        <ul v-else class="list-disc space-y-1 pl-5 text-sm">
          <li v-if="c.policy.soft_delete">{{ t('config.collections.softDelete', { retention: prettyDuration(c.policy.soft_delete.retention) }) }}</li>
          <li v-if="c.policy.on_owner_delete">
            {{ t('config.collections.onOwnerDelete', { action: c.policy.on_owner_delete.action }) }}
            <ul class="list-none pl-0 text-xs">
              <li v-if="hasValues(c.policy.on_owner_delete.remove)">{{ t('config.collections.remove') }}: <code class="font-mono">{{ c.policy.on_owner_delete.remove?.join(', ') }}</code></li>
              <li v-if="hasValues(c.policy.on_owner_delete.replace)">{{ t('config.collections.replace') }}: <code class="font-mono">{{ JSON.stringify(c.policy.on_owner_delete.replace) }}</code></li>
              <li v-if="hasValues(c.policy.on_owner_delete.pull)">{{ t('config.collections.pull') }}: <code class="font-mono">{{ JSON.stringify(c.policy.on_owner_delete.pull) }}</code></li>
              <li v-if="hasValues(c.policy.on_owner_delete.unset)">{{ t('config.collections.unset') }}: <code class="font-mono">{{ JSON.stringify(c.policy.on_owner_delete.unset) }}</code></li>
            </ul>
          </li>
        </ul>
        <ConfigDraft v-if="c.collection_file && (c.collection_yaml || c.rules)" :path="c.collection_file" :initial="collectionFileText(c.collection_yaml, c.rules)" kind="yaml" :label="`${c.name} collection.yaml`" />
      </details>
    </section>
    <p class="text-sm text-slate-600 dark:text-slate-400">
      {{ t('config.collections.functionsLink') }}
      <RouterLink v-if="cfg.config" :to="{ name: 'functions', params: { realm } }" class="underline">{{ t('nav.functions') }}</RouterLink>
    </p>
  </ConfigFrame>
</template>
