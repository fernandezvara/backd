<script setup lang="ts">
import { AuthenticationError, NetworkError } from 'backd-js'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import { safeNext } from '@/lib/redirect'
import { NotAdminError, useSession } from '@/stores/session'

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()
const router = useRouter()
const route = useRoute()

const email = ref('')
const password = ref('')
const keep = ref(false)
const busy = ref(false)
const error = ref('')

// Only the realm's own pages are followed after signing in.
const next = () => safeNext(route.query.next, props.realm)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await session.signIn(props.realm, email.value.trim(), password.value, keep.value)
    password.value = ''
    await router.replace(next())
  } catch (e) {
    error.value =
      e instanceof NotAdminError ? t('signIn.notAdmin') : e instanceof NetworkError ? t('signIn.unreachable') : e instanceof AuthenticationError ? t('signIn.failed') : t('signIn.failed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="mx-auto max-w-sm space-y-6 px-4 py-16">
    <div class="flex items-center justify-between">
      <span class="font-semibold">{{ t('app.name') }}</span>
      <ThemeSwitch />
    </div>
    <div class="rounded-md bg-brand-50 px-3 py-2 text-brand-800 dark:bg-brand-800 dark:text-brand-50" data-testid="realm-badge">
      <span class="text-xs uppercase tracking-wide">{{ t('realm.label') }}</span>
      <p class="text-lg font-semibold">{{ realm }}</p>
    </div>
    <h1 class="text-xl font-semibold">{{ t('signIn.title', { realm }) }}</h1>
    <AppAlert v-if="session.ended">{{ t(`signIn.ended.${session.ended}`) }}</AppAlert>
    <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
    <form class="space-y-4" @submit.prevent="submit">
      <TextField v-model="email" type="email" :label="t('signIn.email')" autocomplete="username" required />
      <TextField v-model="password" type="password" :label="t('signIn.password')" autocomplete="current-password" required />
      <div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="keep" type="checkbox" />
          {{ t('signIn.keep') }}
        </label>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('signIn.keepHelp') }}</p>
      </div>
      <AppButton type="submit" :busy="busy">{{ busy ? t('signIn.submitting') : t('signIn.submit') }}</AppButton>
    </form>
    <RouterLink :to="{ name: 'realm' }" class="text-sm underline">{{ t('signIn.otherRealm') }}</RouterLink>
  </main>
</template>
