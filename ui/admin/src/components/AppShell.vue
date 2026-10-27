<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted, watchEffect } from 'vue'
import { useRoute } from 'vue-router'
import { crumbsFor } from '@/lib/crumbs'
import { visibleNav } from '@/lib/nav'
import { useRealmConfig } from '@/stores/config'
import { useCrumbNames } from '@/stores/crumbs'
import { useSession } from '@/stores/session'
import AccountControls from './AccountControls.vue'
import AccountMenu from './AccountMenu.vue'
import AppBreadcrumb from './AppBreadcrumb.vue'
import AppNav from './AppNav.vue'
import RealmMenu from './RealmMenu.vue'

const { t } = useI18n()
const session = useSession()
const cfg = useRealmConfig()
const route = useRoute()
const names = useCrumbNames()
onMounted(() => void cfg.load())

// Organizations show when the realm has them; a level that can't read the configuration is
// offered the page and the server decides.
const orgsEnabled = computed(() => (cfg.config ? !!cfg.config.settings.organizations : !session.canRead('config')))

const groups = computed(() =>
  visibleNav({ canRead: session.canRead, orgs: orgsEnabled.value, storage: !!cfg.config?.settings.storage, push: !!cfg.config?.settings.push }),
)

const crumbs = computed(() =>
  typeof route.name === 'string' ? crumbsFor(route.name, route.params, { t, realm: session.realm, groups: groups.value, name: names.get }) : null,
)
// The tab says where you are too.
watchEffect(() => {
  const here = crumbs.value?.at(-1)
  document.title = here ? `${here.label} · ${t('app.name')}` : t('app.name')
})
</script>

<template>
  <a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:m-2 focus:rounded focus:bg-white focus:p-2 focus:text-slate-900">{{ t('app.skip') }}</a>
  <header class="sticky top-0 z-40 border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
    <div class="relative mx-auto flex h-14 max-w-6xl items-center gap-3 px-4">
      <AppNav :groups="groups" class="min-[900px]:order-3">
        <template #mobile>
          <div class="border-t border-slate-200 pt-3 dark:border-slate-800">
            <AccountControls />
          </div>
        </template>
      </AppNav>
      <span class="shrink-0 font-semibold">{{ t('app.name') }}</span>
      <RealmMenu />
      <div class="order-4 ml-auto hidden items-center min-[900px]:flex">
        <AccountMenu />
      </div>
    </div>
  </header>
  <AppBreadcrumb v-if="crumbs" :crumbs="crumbs" />
  <main id="main" class="mx-auto max-w-6xl px-4 py-6">
    <slot />
  </main>
</template>
