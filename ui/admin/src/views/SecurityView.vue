<script setup lang="ts">
import { PhDeviceMobile } from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import PasswordsPanel from '@/components/PasswordsPanel.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { errorText } from '@/lib/format'
import { useSession } from '@/stores/session'

// Breach response (expire passwords, tell users, end every session: the same panel the users page
// had) and who still has to set a second factor up.
defineProps<{ realm: string }>()
const { t } = useI18n()
const raw = useRawAdmin()
const session = useSession()

interface Mfa {
  enabled: boolean
  required_all: boolean
  required_roles: string[]
  users_without_mfa: { id: string; email: string; roles: string[] }[]
  total_without_mfa: number
}
const mfa = ref<Mfa>()
const error = ref('')
onMounted(async () => {
  try {
    mfa.value = await raw<Mfa>('GET', ['mfa'])
  } catch (e) {
    error.value = errorText(e, t('security.mfa.loadFailed'))
  }
})
const full = computed(() => session.canWrite('users') && session.access?.level === 'full')
</script>

<template>
  <PageHeader :title="t('security.title')" :description="t('security.description')" />

  <section class="mb-6" aria-labelledby="breach-h">
    <h2 id="breach-h" class="mb-2 text-sm font-semibold">{{ t('security.breach') }}</h2>
    <PasswordsPanel v-if="full" :realm="realm" />
    <AppAlert v-else>{{ t('security.needsFull') }}</AppAlert>
  </section>

  <section aria-labelledby="mfa-h" data-testid="mfa-coverage">
    <h2 id="mfa-h" class="mb-2 text-sm font-semibold">{{ t('security.mfa.title') }}</h2>
    <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
    <AppCard v-else-if="mfa" flush>
      <div class="flex flex-wrap items-center gap-3 border-b border-slate-200 p-3 text-sm dark:border-slate-800">
        <PhDeviceMobile :size="18" aria-hidden="true" class="text-amber-700" />
        <span v-if="!mfa.enabled" class="flex-1">{{ t('security.mfa.off') }}</span>
        <span v-else class="flex-1">
          {{ t('security.mfa.required', { n: mfa.total_without_mfa }) }}
          <template v-if="mfa.required_all"> · {{ t('security.mfa.everyone') }}</template>
          <template v-else-if="mfa.required_roles.length"> · {{ t('security.mfa.roles', { roles: mfa.required_roles.join(', ') }) }}</template>
        </span>
      </div>
      <table v-if="mfa.users_without_mfa.length" class="w-full text-left text-[13px]">
        <thead class="text-xs text-slate-500">
          <tr><th class="px-3 py-1.5 font-medium">{{ t('security.mfa.user') }}</th><th class="px-3 py-1.5 font-medium">{{ t('security.mfa.rolesCol') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="u in mfa.users_without_mfa" :key="u.id" class="border-t border-slate-100 dark:border-slate-800">
            <td class="px-3 py-1.5"><RouterLink :to="{ name: 'user', params: { realm, id: u.id } }" class="underline">{{ u.email }}</RouterLink></td>
            <td class="px-3 py-1.5"><AppBadge v-for="r in u.roles" :key="r" class="mr-1">{{ r }}</AppBadge></td>
          </tr>
        </tbody>
      </table>
      <p v-else class="p-3 text-sm text-slate-500">{{ t('security.mfa.none') }}</p>
      <p v-if="mfa.total_without_mfa > mfa.users_without_mfa.length" class="border-t border-slate-200 p-3 text-xs text-slate-500 dark:border-slate-800">{{ t('security.mfa.more', { shown: mfa.users_without_mfa.length, total: mfa.total_without_mfa }) }}</p>
    </AppCard>
  </section>
</template>
