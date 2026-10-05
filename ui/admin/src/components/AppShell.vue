<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import AppButton from './AppButton.vue'
import ThemeSwitch from './ThemeSwitch.vue'

const { t } = useI18n()
const session = useSession()
const router = useRouter()

const links = computed(() => [
  { name: 'home', label: t('nav.overview'), show: true },
  { name: 'users', label: t('nav.users'), show: session.canRead('users') },
  { name: 'invitations', label: t('nav.invitations'), show: session.canRead('invitations') },
  { name: 'apikeys', label: t('nav.apikeys'), show: session.canRead('apikeys') },
  { name: 'data', label: t('nav.data'), show: session.canRead('data') },
  { name: 'functions', label: t('nav.functions'), show: session.canRead('functions') },
  { name: 'secrets', label: t('nav.secrets'), show: session.canRead('secrets') },
  { name: 'audit', label: t('nav.audit'), show: session.canRead('audit') },
  { name: 'config', label: t('nav.config'), show: session.canRead('config') },
].filter((l) => l.show))

async function signOut() {
  const realm = session.realm
  await session.signOut()
  await router.push({ name: 'signin', params: { realm } })
}
</script>

<template>
  <a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:m-2 focus:rounded focus:bg-white focus:p-2 focus:text-slate-900">{{ t('app.skip') }}</a>
  <header class="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <div class="mx-auto flex max-w-6xl flex-wrap items-center gap-3 px-4 py-3">
      <span class="font-semibold">{{ t('app.name') }}</span>
      <span class="rounded-md bg-brand-50 px-2.5 py-1 text-sm font-semibold text-brand-800 dark:bg-brand-800 dark:text-brand-50" data-testid="realm-badge">
        {{ t('realm.current', { name: session.realm }) }}
      </span>
      <div class="ml-auto flex items-center gap-3">
        <span v-if="session.email" class="text-sm text-slate-600 dark:text-slate-300">{{ session.email }}</span>
        <ThemeSwitch />
        <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
      </div>
    </div>
  </header>
  <nav :aria-label="t('nav.label')" class="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <ul class="mx-auto flex max-w-6xl gap-1 px-4">
      <li v-for="link in links" :key="link.name">
        <RouterLink
          :to="{ name: link.name, params: { realm: session.realm } }"
          class="block border-b-2 border-transparent px-3 py-2 text-sm"
          :active-class="link.name === 'home' ? '' : 'border-brand-700 font-semibold'"
          exact-active-class="border-brand-700 font-semibold"
        >
          {{ link.label }}
        </RouterLink>
      </li>
    </ul>
  </nav>
  <main id="main" class="mx-auto max-w-6xl px-4 py-6">
    <slot />
  </main>
</template>
