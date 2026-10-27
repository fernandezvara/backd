<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { visibleNav } from '@/lib/nav'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'
import AppButton from './AppButton.vue'
import AppNav from './AppNav.vue'
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

const groups = computed(() =>
  visibleNav({ canRead: session.canRead, orgs: orgsEnabled.value, storage: !!cfg.config?.settings.storage, push: !!cfg.config?.settings.push }),
)

async function signOut() {
  const realm = session.realm
  await session.signOut()
  await router.push({ name: 'signin', params: { realm } })
}
</script>

<template>
  <a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:m-2 focus:rounded focus:bg-white focus:p-2 focus:text-slate-900">{{ t('app.skip') }}</a>
  <header class="sticky top-0 z-40 border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <div class="relative mx-auto flex h-14 max-w-6xl items-center gap-3 px-4">
      <AppNav :groups="groups" class="min-[900px]:order-3">
        <template #mobile>
          <div class="flex flex-wrap items-center gap-3 border-t border-slate-200 pt-3 dark:border-slate-800">
            <span v-if="session.email" class="text-sm text-slate-600 dark:text-slate-300">{{ session.email }}</span>
            <LocaleSwitch />
            <ThemeSwitch />
            <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
          </div>
        </template>
      </AppNav>
      <span class="shrink-0 font-semibold">{{ t('app.name') }}</span>
      <span class="shrink-0 rounded-md bg-brand-50 px-2.5 py-1 text-sm font-semibold text-brand-800 dark:bg-brand-800 dark:text-brand-50" data-testid="realm-badge">
        {{ t('realm.current', { name: session.realm }) }}
      </span>
      <div class="order-4 ml-auto hidden items-center gap-2 min-[900px]:flex">
        <LocaleSwitch />
        <ThemeSwitch />
        <AppButton variant="secondary" @click="signOut">{{ t('common.signOut') }}</AppButton>
      </div>
    </div>
  </header>
  <main id="main" class="mx-auto max-w-6xl px-4 py-6">
    <slot />
  </main>
</template>
