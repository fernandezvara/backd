<script setup lang="ts">
import { PhCheckCircle, PhKey, PhLightning, PhSealWarning, PhWarning, PhDeviceMobile } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useAdmin } from '@/lib/admin'
import { useRawAdmin } from '@/lib/adminRaw'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'

// What a person sees first: the realm's configuration at a glance, what needs attention (found with
// the lists the other pages use) and what the signed-in account may do.
const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()
const cfg = useRealmConfig()
const admin = useAdmin()
const raw = useRawAdmin()

const who = computed(() => session.email || t('home.key', { name: session.access?.key ?? '' }))
const areaName = (a: string) => t(`areas.${a}`)

type Db = { collections?: unknown[]; functions?: unknown[] }
const dbs = computed(() => Object.values((cfg.config?.databases ?? {}) as Record<string, Db>))
const collections = computed(() => dbs.value.reduce((n, d) => n + (d.collections?.length ?? 0), 0))
const functions = computed(() => dbs.value.reduce((n, d) => n + (d.functions?.length ?? 0), 0))

interface Attention {
  id: string
  icon: unknown
  text: string
  to: { name: string }
}
const attention = ref<Attention[]>([])
const loaded = ref(false)
const guard = async (f: () => Promise<void>) => {
  try {
    await f()
  } catch {
    // one source being unreachable must not hide the others
  }
}
onMounted(async () => {
  void cfg.load()
  const found: Attention[] = []
  const jobs: Promise<void>[] = []
  if (session.canRead('functions')) {
    jobs.push(
      guard(async () => {
        const failed = (await admin.invocations.list({ limit: 50 })).items.filter((i) => i.status !== 'ok')
        if (failed.length) found.push({ id: 'runs', icon: PhLightning, text: t('home.attention.runs', { n: failed.length, name: failed[0].function }), to: { name: 'functions-history' } })
      }),
      guard(async () => {
        const gaps = await raw<{ items: unknown[] }>('GET', ['triggers', 'gaps'])
        if (gaps.items.length) found.push({ id: 'gaps', icon: PhSealWarning, text: t('home.attention.gaps', { n: gaps.items.length }), to: { name: 'functions' } })
      }),
    )
  }
  if (session.canRead('apikeys')) {
    jobs.push(
      guard(async () => {
        const soon = Date.now() + 14 * 86400_000
        const keys = (await admin.apiKeys.list()).filter((k) => k.expires_at && Date.parse(k.expires_at) < soon)
        if (keys.length) found.push({ id: 'keys', icon: PhKey, text: t('home.attention.keys', { n: keys.length, name: keys[0].name }), to: { name: 'apikeys' } })
      }),
    )
  }
  if (session.canRead('users')) {
    jobs.push(
      guard(async () => {
        const mfa = await raw<{ enabled: boolean; total_without_mfa: number }>('GET', ['mfa'])
        if (mfa.enabled && mfa.total_without_mfa) found.push({ id: 'mfa', icon: PhDeviceMobile, text: t('home.attention.mfa', { n: mfa.total_without_mfa }), to: { name: 'security' } })
      }),
    )
  }
  await Promise.all(jobs)
  attention.value = found
  loaded.value = true
})
const warnings = computed(() => cfg.config?.warnings ?? [])
</script>

<template>
  <PageHeader :title="t('home.title')" :description="t('home.signedInAs', { who })">
    <RouterLink v-if="session.canRead('data')" :to="{ name: 'data', params: { realm: props.realm } }"><AppButton>{{ t('home.openData') }}</AppButton></RouterLink>
  </PageHeader>

  <div class="mb-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
    <AppCard v-if="cfg.config">
      <p class="text-xs text-slate-500">{{ t('home.collections') }}</p>
      <p class="mt-1 text-2xl font-semibold" data-testid="stat-collections">{{ collections }}</p>
    </AppCard>
    <AppCard v-if="cfg.config">
      <p class="text-xs text-slate-500">{{ t('home.functions') }}</p>
      <p class="mt-1 text-2xl font-semibold" data-testid="stat-functions">{{ functions }}</p>
    </AppCard>
    <AppCard class="sm:col-span-2" v-if="cfg.config">
      <p class="flex items-center gap-2 text-sm font-medium"><PhCheckCircle :size="18" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />{{ t('home.loaded') }}</p>
      <p class="mt-1 truncate font-mono text-xs text-slate-500">{{ cfg.config.fingerprint }} · {{ cfg.config.file }}</p>
    </AppCard>
  </div>

  <div class="grid gap-4 lg:grid-cols-2">
    <AppCard flush>
      <h2 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('home.attention.title') }}</h2>
      <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800" data-testid="attention">
        <li v-for="w in warnings" :key="w.code" class="flex items-start gap-3 px-4 py-2.5"><PhWarning :size="16" class="mt-0.5 shrink-0 text-amber-700" aria-hidden="true" />{{ w.message }}</li>
        <li v-for="a in attention" :key="a.id">
          <RouterLink :to="{ ...a.to, params: { realm: props.realm } }" class="flex items-start gap-3 px-4 py-2.5 hover:bg-slate-50 dark:hover:bg-slate-800">
            <component :is="a.icon" :size="16" class="mt-0.5 shrink-0 text-amber-700" aria-hidden="true" />{{ a.text }}
          </RouterLink>
        </li>
        <li v-if="loaded && !warnings.length && !attention.length" class="px-4 py-3 text-slate-500">{{ t('home.attention.none') }}</li>
      </ul>
    </AppCard>
    <AppCard>
      <h2 class="mb-2 text-[13px] font-semibold">{{ t('home.access') }}</h2>
      <dl class="space-y-2 text-[13px]">
        <div class="flex gap-3"><dt class="w-24 shrink-0 text-slate-500">{{ t('home.level') }}</dt><dd data-testid="level">{{ t(`home.levels.${session.access?.level ?? 'read'}`) }}</dd></div>
        <div class="flex gap-3"><dt class="w-24 shrink-0 text-slate-500">{{ t('home.canRead') }}</dt><dd data-testid="can-read">{{ session.readable.length ? session.readable.map(areaName).join(', ') : t('home.nothing') }}</dd></div>
        <div class="flex gap-3"><dt class="w-24 shrink-0 text-slate-500">{{ t('home.canWrite') }}</dt><dd data-testid="can-write">{{ session.writable.length ? session.writable.map(areaName).join(', ') : t('home.nothing') }}</dd></div>
      </dl>
    </AppCard>
  </div>
  <AppAlert v-if="cfg.error" kind="error" class="mt-4">{{ cfg.error }}</AppAlert>
</template>
