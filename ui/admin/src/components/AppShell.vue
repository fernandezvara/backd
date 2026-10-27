<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { visibleNav } from '@/lib/nav'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import AppButton from './AppButton.vue'
import LocaleSwitch from './LocaleSwitch.vue'
import ThemeSwitch from './ThemeSwitch.vue'

const { t } = useI18n()
const session = useSession()
const router = useRouter()
const cfg = useRealmConfig()
onMounted(() => void cfg.load())

// Organizations show when the realm has them; a level that can't read the configuration is
// offered the page and the server decides.
const orgsEnabled = computed(() => (cfg.config ? !!cfg.config.settings.organizations : !session.canRead('config')))

// The flat bar shows one link per page; the Jobs and history item has its own tab under Functions.
const links = computed(() =>
  visibleNav({ canRead: session.canRead, orgs: orgsEnabled.value, storage: !!cfg.config?.settings.storage, push: !!cfg.config?.settings.push })
    .flatMap((g) => g.items)
    .filter((i) => i.route !== 'functions-jobs'),
)

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
        <LocaleSwitch />
        <ThemeSwitch />
        <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
      </div>
    </div>
  </header>
  <nav :aria-label="t('nav.label')" class="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <ul class="mx-auto flex max-w-6xl gap-1 px-4">
      <li v-for="link in links" :key="link.route">
        <RouterLink
          :to="{ name: link.route, params: { realm: session.realm } }"
          class="block border-b-2 border-transparent px-3 py-2 text-sm"
          :active-class="link.route === 'home' ? '' : 'border-brand-700 font-semibold'"
          exact-active-class="border-brand-700 font-semibold"
        >
          {{ t(link.label) }}
        </RouterLink>
      </li>
    </ul>
  </nav>
  <main id="main" class="mx-auto max-w-6xl px-4 py-6">
    <slot />
  </main>
</template>
