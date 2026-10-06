<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
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
  policy?: { soft_delete?: unknown }
}
const databases = computed(() => {
  const dbs = (cfg.config?.databases ?? {}) as Record<string, { collections?: Col[] }>
  return Object.entries(dbs)
    .map(([name, db]) => ({ name, collections: db.collections ?? [] }))
    .filter((db) => db.collections.length)
    .sort((a, b) => a.name.localeCompare(b.name))
})

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
  <h1 class="text-2xl font-semibold">{{ t('data.title') }}</h1>
  <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('data.help') }}</p>
  <p class="mt-2 text-sm"><RouterLink :to="{ name: 'data-checks', params: { realm } }" class="underline" data-testid="open-checks">{{ t('checks.title') }}</RouterLink></p>

  <template v-if="knows">
    <AppAlert v-if="cfg.error" kind="error" class="mt-4">{{ cfg.error }}</AppAlert>
    <p v-else-if="!cfg.config" class="mt-4 text-sm" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="!databases.length" class="mt-4 text-sm text-slate-600 dark:text-slate-400">{{ t('data.noCollections') }}</p>
    <section v-for="db in databases" :key="db.name" class="mt-6" :aria-labelledby="`ddb-${db.name}`">
      <h2 :id="`ddb-${db.name}`" class="text-lg font-semibold">{{ t('data.databases', { name: db.name }) }}</h2>
      <ul class="mt-2 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <li v-for="c in db.collections" :key="c.name">
          <RouterLink
            :to="{ name: 'data-collection', params: { realm, database: db.name, collection: c.name } }"
            class="block rounded-lg border border-slate-200 bg-white p-4 hover:border-brand-700 dark:border-slate-800 dark:bg-slate-900"
            :data-testid="`open-${db.name}-${c.name}`"
          >
            <span class="font-mono text-sm font-semibold">{{ c.name }}</span>
            <span v-if="c.policy?.soft_delete" class="ml-2 text-xs text-slate-600 dark:text-slate-400">{{ t('data.softDeleting') }}</span>
          </RouterLink>
        </li>
      </ul>
    </section>
  </template>
  <form v-else class="mt-4 max-w-md space-y-4" @submit.prevent="open">
    <AppAlert>{{ t('data.collectionsNeedConfig') }}</AppAlert>
    <TextField v-model="database" :label="t('data.databaseLabel')" autocomplete="off" required />
    <TextField v-model="collection" :label="t('data.collectionLabel')" autocomplete="off" required />
    <AppButton type="submit">{{ t('data.open') }}</AppButton>
  </form>
</template>
