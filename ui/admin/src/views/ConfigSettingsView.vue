<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { flatten } from '@/lib/flatten'
import { useRealmConfig } from '@/stores/config'

defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()

interface Role {
  description?: string
  admin?: string
  seeded_users?: number
  seeded_emails?: string[]
}
const roles = computed(() => Object.entries((cfg.config?.settings.roles ?? {}) as Record<string, Role>))
// Roles have their own table; every other setting is a dotted row.
const rows = computed(() => {
  const { roles: _roles, ...rest } = (cfg.config?.settings ?? {}) as Record<string, unknown>
  void _roles
  return flatten(rest)
})
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.settings')">
    <template #default="{ config }">
      <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.settings.file', { file: config.file }) }}</p>

      <section class="mt-4" aria-labelledby="settings-h">
        <h2 id="settings-h" class="text-lg font-semibold">{{ t('config.settings.title') }}</h2>
        <div class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table class="w-full text-left text-sm" data-testid="settings-table">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('config.settings.setting') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.settings.value') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.path" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <th scope="row" class="px-3 py-1.5 font-mono text-xs font-normal">{{ r.path }}</th>
                <td class="px-3 py-1.5 break-all">{{ r.value || t('config.settings.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t('config.settings.secretNote') }}</p>
      </section>

      <section class="mt-8" aria-labelledby="roles-h">
        <h2 id="roles-h" class="text-lg font-semibold">{{ t('config.roles.title') }}</h2>
        <p v-if="!roles.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ t('config.roles.none') }}</p>
        <div v-else class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table class="w-full text-left text-sm" data-testid="roles-table">
            <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.name') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.description') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.admin') }}</th>
                <th scope="col" class="px-3 py-2">{{ t('config.roles.seeded') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="[name, role] in roles" :key="name" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
                <th scope="row" class="px-3 py-2 font-mono text-xs font-medium">{{ name }}</th>
                <td class="px-3 py-2">{{ role.description || '—' }}</td>
                <td class="px-3 py-2">{{ role.admin && role.admin !== 'none' ? role.admin : '—' }}</td>
                <td class="px-3 py-2">{{ role.seeded_emails ? (role.seeded_emails.length ? role.seeded_emails.join(', ') : '—') : (role.seeded_users ?? 0) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </ConfigFrame>
</template>
