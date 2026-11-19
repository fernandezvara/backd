<script setup lang="ts">
import { PhMagnifyingGlass } from '@phosphor-icons/vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { flatten } from '@/lib/flatten'
import { useRealmConfig } from '@/stores/config'

// Every setting of the realm as the server runs it: the defaults that apply, and the sections the realm
// doesn't configure, said as such. Roles are below. It is realm.yaml, read; it is changed in the repository.
defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()

interface Role {
  description?: string
  admin?: string
  seeded_users?: number
  seeded_emails?: string[]
  strategies?: string[]
}
const settings = computed(() => (cfg.config?.settings ?? {}) as Record<string, unknown>)
const roles = computed(() => Object.entries((settings.value.roles ?? {}) as Record<string, Role>))

// The sections, in the order realm.yaml reads best. `optional` ones exist only when the realm turns them on.
const SECTIONS: { id: string; keys: string[]; optional?: boolean }[] = [
  { id: 'general', keys: ['auth', 'signup', 'default_locale', 'locales', 'cors', 'openapi', 'pages', 'app_config'] },
  { id: 'signin', keys: ['strategies', 'providers', 'passkeys', 'totp', 'magic_link', 'sign_in'] },
  { id: 'sessions', keys: ['sessions', 'password', 'login_throttle'] },
  { id: 'account', keys: ['account', 'consents', 'compliance'] },
  { id: 'admin', keys: ['admin', 'audit'] },
  { id: 'functions', keys: ['functions', 'triggers', 'realtime', 'rate_limit'] },
  { id: 'organizations', keys: ['organizations'], optional: true },
  { id: 'email', keys: ['email'], optional: true },
  { id: 'push', keys: ['push'], optional: true },
  { id: 'storage', keys: ['storage'], optional: true },
  { id: 'public_storage', keys: ['public_storage'], optional: true },
]

const filter = ref('')
const sections = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const known = new Set(SECTIONS.flatMap((s) => s.keys).concat('roles'))
  const all = [...SECTIONS, { id: 'other', keys: Object.keys(settings.value).filter((k) => !known.has(k)) }]
  return all
    .map((s) => {
      const configured = s.keys.some((k) => settings.value[k] !== undefined)
      const rows = s.keys.flatMap((k) => (settings.value[k] === undefined ? [] : flatten(settings.value[k], k)))
      return { ...s, configured, rows: q ? rows.filter((r) => r.path.toLowerCase().includes(q) || r.value.toLowerCase().includes(q)) : rows }
    })
    .filter((s) => (q ? s.rows.length : s.keys.length))
})
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.settings')">
    <template #default="{ config }">
      <div class="mb-3 flex flex-wrap items-center gap-3">
        <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.settings.file', { file: config.file }) }} · {{ t('config.settings.defaultsNote') }}</p>
        <div class="relative ml-auto">
          <PhMagnifyingGlass :size="14" class="pointer-events-none absolute left-2.5 top-2.5 text-slate-400" aria-hidden="true" />
          <input v-model="filter" type="search" class="h-8 w-64 max-w-full rounded-md border border-slate-300 bg-white pl-8 pr-2 text-[13px] dark:border-slate-700 dark:bg-slate-900" :placeholder="t('config.settings.filter')" :aria-label="t('config.settings.filter')" autocomplete="off" />
        </div>
      </div>

      <div class="space-y-4" data-testid="settings-sections">
        <section v-for="s in sections" :key="s.id" :aria-labelledby="`set-${s.id}`" :data-testid="`settings-${s.id}`">
          <AppCard flush>
            <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-2.5 dark:border-slate-800">
              <h2 :id="`set-${s.id}`" class="text-sm font-semibold">{{ t(`config.settings.sections.${s.id}`) }}</h2>
              <AppBadge v-if="s.optional && !s.configured" class="ml-1">{{ t('config.settings.notConfigured') }}</AppBadge>
            </div>
            <p v-if="s.optional && !s.configured" class="px-4 py-3 text-[13px] text-slate-600 dark:text-slate-400">{{ t(`config.settings.absent.${s.id}`) }}</p>
            <table v-else class="w-full text-left text-sm">
              <thead class="sr-only">
                <tr><th scope="col">{{ t('config.settings.setting') }}</th><th scope="col">{{ t('config.settings.value') }}</th></tr>
              </thead>
              <tbody>
                <tr v-for="r in s.rows" :key="r.path" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                  <th scope="row" class="w-[22rem] max-w-[45%] px-4 py-1.5 align-top font-mono text-xs font-normal text-slate-600 break-all dark:text-slate-400">{{ r.path }}</th>
                  <td class="px-4 py-1.5 break-all">{{ r.value || t('config.settings.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </AppCard>
        </section>
      </div>
      <p class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t('config.settings.secretNote') }}</p>

      <section class="mt-8" aria-labelledby="roles-h">
        <h2 id="roles-h" class="text-lg font-semibold">{{ t('config.roles.title') }}</h2>
        <p v-if="!roles.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('config.roles.none') }}</p>
        <AppCard v-else flush class="mt-2">
          <table class="w-full text-left text-sm" data-testid="roles-table">
            <thead>
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.name') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.description') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.admin') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.strategies') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.seeded') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="[name, role] in roles" :key="name" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
                <th scope="row" class="px-3 py-2 font-mono text-xs font-medium">{{ name }}</th>
                <td class="px-3 py-2">{{ role.description || '—' }}</td>
                <td class="px-3 py-2">{{ role.admin && role.admin !== 'none' ? role.admin : '—' }}</td>
                <td class="px-3 py-2">{{ role.strategies?.length ? role.strategies.join(', ') : t('config.roles.allStrategies') }}</td>
                <td class="px-3 py-2">{{ role.seeded_emails ? (role.seeded_emails.length ? role.seeded_emails.join(', ') : '—') : (role.seeded_users ?? 0) }}</td>
              </tr>
            </tbody>
          </table>
        </AppCard>
      </section>
    </template>
  </ConfigFrame>
</template>
