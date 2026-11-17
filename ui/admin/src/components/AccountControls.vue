<script setup lang="ts">
import { PhSignOut, PhUserCircle } from '@phosphor-icons/vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import AccentSwitch from './AccentSwitch.vue'
import LocaleSwitch from './LocaleSwitch.vue'
import ThemeSwitch from './ThemeSwitch.vue'

// Who is signed in, the accent, language and theme choices, and the way out: the account menu.
const { t } = useI18n()
const session = useSession()
const router = useRouter()

async function signOut() {
  const realm = session.realm
  await session.signOut()
  await router.push({ name: 'signin', params: { realm } })
}
</script>

<template>
  <div class="space-y-3">
    <div class="border-b border-slate-200 pb-2 dark:border-slate-800">
      <p v-if="session.email" class="text-sm break-all">{{ session.email }}</p>
      <p v-if="session.strategy" class="text-xs text-slate-600 dark:text-slate-400" data-testid="session-strategy">{{ t('common.strategy', { name: session.strategy }) }}</p>
    </div>
    <RouterLink v-if="session.signedIn" :to="{ name: 'account', params: { realm: session.realm } }" class="flex items-center gap-2 text-sm hover:underline" data-testid="account-link"><PhUserCircle :size="16" aria-hidden="true" />{{ t('account.title') }}</RouterLink>
    <AccentSwitch />
    <LocaleSwitch />
    <ThemeSwitch />
    <button type="button" class="flex w-full items-center gap-2 rounded-md px-1 py-1 text-sm hover:bg-slate-100 dark:hover:bg-slate-800" data-testid="sign-out" @click="signOut"><PhSignOut :size="16" aria-hidden="true" />{{ t('common.signOut') }}</button>
  </div>
</template>
