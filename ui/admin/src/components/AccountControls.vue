<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import AppButton from './AppButton.vue'
import { RouterLink } from 'vue-router'
import LocaleSwitch from './LocaleSwitch.vue'
import ThemeSwitch from './ThemeSwitch.vue'

// Who is signed in, the language and theme choices, and the way out: in the account menu on a
// wide screen and at the foot of the menu panel on a narrow one.
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
  <div class="flex flex-col items-end gap-3">
    <p v-if="session.email" class="text-sm break-all">{{ session.email }}</p>
    <p v-if="session.strategy" class="text-xs text-slate-600 dark:text-slate-400" data-testid="session-strategy">{{ t('common.strategy', { name: session.strategy }) }}</p>
    <RouterLink v-if="session.signedIn" :to="{ name: 'account', params: { realm: session.realm } }" class="text-sm underline" data-testid="account-link">{{ t('account.title') }}</RouterLink>
    <LocaleSwitch />
    <ThemeSwitch />
    <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
  </div>
</template>
