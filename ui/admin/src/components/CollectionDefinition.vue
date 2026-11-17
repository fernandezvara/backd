<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import { useRealmConfig } from '@/stores/config'

// What the configuration says about this collection: its rules, indexes and the file that defines them.
// Read-only: a change is a file in the configuration repository (the Configuration page prepares it).
const props = defineProps<{ realm: string; database: string; collection: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()
onMounted(() => void cfg.load())

interface CollectionConfig {
  name: string
  indexes: { fields: string; unique: boolean; ttl: boolean }[]
  rules?: Record<string, { expression: string; from: string }>
  collection_file?: string
  schema_file: string
}
const c = computed(() => {
  const dbs = (cfg.config?.databases ?? {}) as Record<string, { collections?: CollectionConfig[] }>
  return dbs[props.database]?.collections?.find((x) => x.name === props.collection)
})
const OPS = ['read', 'create', 'update', 'delete', 'restore', 'purge']
</script>

<template>
  <AppAlert v-if="!cfg.config">{{ t('data.definition.needsConfig') }}</AppAlert>
  <AppAlert v-else-if="!c" kind="error">{{ t('data.definition.missing') }}</AppAlert>
  <div v-else class="grid gap-5 lg:grid-cols-2" data-testid="collection-definition">
    <AppCard flush>
      <h3 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('data.definition.rules') }}</h3>
      <table class="w-full text-left text-[13px]">
        <tbody>
          <tr v-for="op in OPS" :key="op" class="border-t border-slate-100 first:border-0 dark:border-slate-800">
            <td class="w-24 px-4 py-2 font-medium">{{ op }}</td>
            <td class="px-2 py-2 font-mono text-xs">{{ c.rules?.[op]?.expression ?? '—' }}<AppBadge v-if="c.rules?.[op] && c.rules[op].from !== op" class="ml-1">{{ t('data.definition.from', { op: c.rules[op].from }) }}</AppBadge></td>
          </tr>
        </tbody>
      </table>
      <p class="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-slate-800">{{ c.collection_file }}</p>
    </AppCard>
    <AppCard flush>
      <h3 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('data.definition.indexes') }}</h3>
      <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
        <li v-for="ix in c.indexes" :key="ix.fields" class="flex items-center gap-2 px-4 py-2"><span class="font-mono">{{ ix.fields }}</span><AppBadge v-if="ix.unique">{{ t('data.definition.unique') }}</AppBadge><AppBadge v-if="ix.ttl">TTL</AppBadge></li>
        <li v-if="!c.indexes.length" class="px-4 py-3 text-slate-500">{{ t('data.definition.noIndexes') }}</li>
      </ul>
      <p class="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-slate-800">{{ c.schema_file }}</p>
    </AppCard>
    <p class="text-xs text-slate-500 lg:col-span-2">{{ t('data.definition.readOnly') }} <RouterLink :to="{ name: 'config-collections', params: { realm } }" class="underline">{{ t('data.definition.open') }}</RouterLink></p>
  </div>
</template>
