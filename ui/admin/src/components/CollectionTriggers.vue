<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText } from '@/lib/format'
import { useSession } from '@/stores/session'

// The triggers the configuration declares on this collection: what fires them and the function they run.
const props = defineProps<{ realm: string; database: string; collection: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()

interface Trigger { database: string; collection: string; name: string; events: string[]; when: string | null; function: string; fields: string[] | null; session: boolean; ordered: boolean }
const triggers = ref<Trigger[] | null>(null)
const error = ref('')
const allowed = computed(() => session.canRead('functions'))
const mine = computed(() => (triggers.value ?? []).filter((x) => x.database === props.database && x.collection === props.collection))
// "main/send_receipt": the function's database and name, for its page.
const target = (fn: string) => {
  const [database, ...rest] = fn.split('/')
  return rest.length ? { database, name: rest.join('/') } : null
}

onMounted(async () => {
  if (!allowed.value) return
  try {
    triggers.value = (await raw<{ triggers: Trigger[] }>('GET', ['triggers', 'status'])).triggers
  } catch (e) {
    error.value = errorText(e, t('triggers.loadFailed'))
  }
})
</script>

<template>
  <AppAlert v-if="!allowed">{{ t('data.triggers.needsFunctions') }}</AppAlert>
  <AppAlert v-else-if="error" kind="error">{{ error }}</AppAlert>
  <p v-else-if="!triggers" class="text-sm" role="status">{{ t('common.loading') }}</p>
  <AppCard v-else flush data-testid="collection-triggers">
    <h3 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('data.triggers.title') }}</h3>
    <table class="w-full text-left text-[13px]">
      <thead>
        <tr>
          <th scope="col" class="px-4 py-2">{{ t('data.triggers.event') }}</th>
          <th scope="col" class="px-2 py-2">{{ t('data.triggers.name') }}</th>
          <th scope="col" class="px-2 py-2">{{ t('data.triggers.function') }}</th>
          <th scope="col" class="px-2 py-2">{{ t('data.triggers.when') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="x in mine" :key="x.name" class="border-t border-slate-100 dark:border-slate-800">
          <td class="px-4 py-2"><span class="flex flex-wrap gap-1"><AppBadge v-for="e in x.events" :key="e" kind="violet">{{ e }}</AppBadge></span></td>
          <td class="px-2 py-2 font-mono text-xs">{{ x.name }}<AppBadge v-if="x.ordered" class="ml-1">{{ t('data.triggers.ordered') }}</AppBadge></td>
          <td class="px-2 py-2 font-mono text-xs">
            <RouterLink v-if="target(x.function)" :to="{ name: 'function', params: { realm, ...target(x.function)! } }" class="text-brand-700 hover:underline dark:text-brand-400">{{ x.function }}</RouterLink>
            <template v-else>{{ x.function }}</template>
          </td>
          <td class="px-2 py-2 font-mono text-xs text-slate-600 dark:text-slate-400">{{ x.when ?? '—' }}</td>
        </tr>
        <tr v-if="!mine.length"><td colspan="4" class="px-4 py-6 text-center text-slate-500">{{ t('data.triggers.none') }}</td></tr>
      </tbody>
    </table>
    <p class="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-slate-800">
      {{ t('data.triggers.readOnly') }} <RouterLink :to="{ name: 'functions-triggers', params: { realm } }" class="underline">{{ t('data.triggers.status') }}</RouterLink>
    </p>
  </AppCard>
</template>
