<script setup lang="ts">
import type { CheckReport, DataMigrationsList, SchemaMigrationReport } from 'backd-js'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

// On the page of a collection that keeps its schema in versions (schema.v<N>.json with migrations):
// where its documents stand, the latest migration, and "Migrate now". The panel is not there for any other collection.
const props = defineProps<{ realm: string; database: string; collection: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()

const listing = ref<DataMigrationsList>()
const item = computed(() => listing.value?.items.find((i) => i.database === props.database && i.collection === props.collection))
const running = computed(() => listing.value?.running.some((j) => j.function === `schema-migrate:${props.database}/${props.collection}`) ?? false)
const full = ref<SchemaMigrationReport | null>(null)
const check = ref<CheckReport | null>(null)
const error = ref('')
const asking = ref(false)
const dryRun = ref(false)
const busy = ref(false)
const canWrite = computed(() => session.canWrite('data'))
let timer: ReturnType<typeof setTimeout> | undefined

async function refresh() {
  try {
    listing.value = await admin.dataMigrations.list()
    error.value = ''
    if (item.value?.report) full.value = await admin.dataMigrations.get(props.database, props.collection).catch(() => null)
    else full.value = null
  } catch (e) {
    error.value = errorText(e, t('schemaVersions.loadFailed'))
  }
  clearTimeout(timer)
  if (running.value) timer = setTimeout(refresh, 2000)
}
onMounted(async () => {
  await refresh()
  check.value = await admin.dataChecks.get(props.database, props.collection).catch(() => null)
})
onBeforeUnmount(() => clearTimeout(timer))

function ask(dry: boolean) {
  dryRun.value = dry
  asking.value = true
}
async function start() {
  busy.value = true
  try {
    const started = await admin.dataMigrations.start({ database: props.database, collection: props.collection, dryRun: dryRun.value })
    toasts.push(started.already_running ? t('schemaVersions.alreadyRunning') : t('schemaVersions.started'))
    asking.value = false
    await refresh()
  } catch (e) {
    error.value = errorText(e, t('schemaVersions.startFailed'))
  } finally {
    busy.value = false
  }
}
const versions = computed(() => Object.entries(check.value?.by_version ?? {}).sort(([a], [b]) => Number(a) - Number(b)))
const ending = (r: SchemaMigrationReport) => t(`schemaVersions.ending.${r.ending ?? 'failed'}`)
</script>

<template>
  <section v-if="item" class="mt-4 space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" :aria-label="t('schemaVersions.panel')" data-testid="schema-versions">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="font-semibold">{{ t('schemaVersions.panel') }}</h2>
      <div v-if="canWrite" class="flex items-center gap-3 text-sm">
        <button type="button" class="underline disabled:opacity-60" :disabled="running" data-testid="migrate-dry" @click="ask(true)">{{ t('schemaVersions.dryRun') }}</button>
        <AppButton variant="secondary" :disabled="running" data-testid="migrate-now" @click="ask(false)">{{ t('schemaVersions.migrate') }}</AppButton>
      </div>
    </div>
    <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
    <p v-if="running" class="text-sm" role="status" data-testid="migration-running">{{ t('schemaVersions.running') }}</p>
    <dl class="grid max-w-xl grid-cols-[14rem_1fr] gap-x-4 gap-y-1 text-sm">
      <dt class="text-slate-600 dark:text-slate-400">{{ t('schemaVersions.current', { n: item.current_version }) }}</dt>
      <dd data-testid="below-current">{{ item.documents_below_current === null ? t('schemaVersions.unknown') : t('schemaVersions.below', { n: item.documents_below_current }) }}</dd>
    </dl>
    <div v-if="versions.length" data-testid="by-version">
      <h3 class="text-sm font-medium">{{ t('schemaVersions.perVersion') }}</h3>
      <ul class="mt-1 flex flex-wrap gap-3 text-sm">
        <li v-for="[v, n] in versions" :key="v" class="font-mono text-xs">v{{ v }}: {{ n }}</li>
      </ul>
    </div>
    <div data-testid="latest-migration">
      <h3 class="text-sm font-medium">{{ t('schemaVersions.latest') }}</h3>
      <p v-if="!full" class="mt-1 text-sm text-slate-600 dark:text-slate-400">{{ t('schemaVersions.never') }}</p>
      <template v-else>
        <p class="mt-1 text-sm">
          {{ ending(full) }}<span v-if="full.dry_run"> · {{ t('schemaVersions.dryRunTag') }}</span> ·
          {{ t('schemaVersions.counts', { scanned: full.scanned, migrated: full.migrated, skipped: full.skipped, failed: full.failed }) }}
        </p>
        <details v-if="full.failures?.length" class="mt-1">
          <summary class="cursor-pointer text-sm underline">{{ t('schemaVersions.failures') }}</summary>
          <ul class="mt-1 space-y-0.5 font-mono text-xs">
            <li v-for="f in full.failures" :key="f.id">{{ t('schemaVersions.failure', { id: f.id, step: f.step, why: [f.kind, f.path, f.keyword].filter(Boolean).join(' ') }) }}</li>
          </ul>
        </details>
      </template>
    </div>
    <div v-if="asking" class="rounded-md border border-slate-300 p-3 text-sm dark:border-slate-700" role="dialog" :aria-label="t('schemaVersions.confirmTitle', { scope: `${database}/${collection}` })" data-testid="migrate-confirm">
      <p class="font-medium">{{ t('schemaVersions.confirmTitle', { scope: `${database}/${collection}` }) }}</p>
      <p class="mt-1">{{ dryRun ? t('schemaVersions.confirmDry') : t('schemaVersions.confirmBody', { n: item.current_version }) }}</p>
      <div class="mt-2 flex gap-2">
        <AppButton :disabled="busy" data-testid="migrate-confirm-go" @click="start">{{ dryRun ? t('schemaVersions.dryRun') : t('schemaVersions.migrate') }}</AppButton>
        <AppButton variant="secondary" :disabled="busy" @click="asking = false">{{ t('common.cancel') }}</AppButton>
      </div>
    </div>
  </section>
</template>
