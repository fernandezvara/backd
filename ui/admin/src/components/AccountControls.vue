<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import AppButton from './AppButton.vue'
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
    <LocaleSwitch />
    <ThemeSwitch />
    <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
  </div>
</template>
