<script setup lang="ts">
import { PhList, PhMagnifyingGlass, PhX } from '@phosphor-icons/vue'
import { computed, onMounted, ref, watch, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { crumbsFor } from '@/lib/crumbs'
import { visibleNav } from '@/lib/nav'
import { useRealmConfig } from '@/stores/config'
import { useCrumbNames } from '@/stores/crumbs'
import { useRealms } from '@/stores/realms'
import { useSession } from '@/stores/session'
import AccountMenu from './AccountMenu.vue'
import ThemeToggle from './ThemeToggle.vue'
import AppBreadcrumb from './AppBreadcrumb.vue'
import AppNav from './AppNav.vue'
import CommandPalette from './CommandPalette.vue'
import RealmSwitcher from './RealmSwitcher.vue'

// The frame of every signed-in page: the accent-tinted sidebar (realm switcher and menu), a thin
// top bar (breadcrumb, search, account) and the page, which scrolls on its own.
const { t } = useI18n()
const session = useSession()
const cfg = useRealmConfig()
const route = useRoute()
const names = useCrumbNames()
const realms = useRealms()
onMounted(() => {
  void cfg.load()
  realms.remember(session.realm)
})

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

const COLLAPSED = 'backd-admin:sidebar'
const collapsed = ref(localStorage.getItem(COLLAPSED) === 'c')
const mobileOpen = ref(false)
const palette = ref(false)
function toggle() {
  collapsed.value = !collapsed.value
  try {
    localStorage.setItem(COLLAPSED, collapsed.value ? 'c' : 'o')
  } catch {
    // not persisted
  }
}
// A page opened from the menu closes the drawer of a narrow screen.
watch(() => route.fullPath, () => (mobileOpen.value = false))
</script>

<template>
  <a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:z-[100] focus:m-2 focus:rounded focus:bg-white focus:p-2 focus:text-slate-900">{{ t('app.skip') }}</a>
  <div class="flex h-screen">
    <div v-if="mobileOpen" class="fixed inset-0 z-40 bg-slate-950/40 min-[900px]:hidden" aria-hidden="true" @click="mobileOpen = false" />
    <aside
      id="side"
      class="side z-50 flex shrink-0 flex-col transition-[width] duration-150 max-[899px]:fixed max-[899px]:inset-y-0 max-[899px]:left-0 max-[899px]:w-64 max-[899px]:transition-transform"
      :class="[collapsed ? 'min-[900px]:w-14' : 'min-[900px]:w-60', mobileOpen ? '' : 'max-[899px]:-translate-x-full']"
    >
      <div class="side-rule flex h-12 items-center gap-2 border-b px-3">
        <span class="font-semibold text-white" :class="collapsed ? 'min-[900px]:sr-only' : ''">{{ t('app.name') }}</span>
        <button type="button" class="ml-auto grid size-7 place-items-center rounded-md text-white/70 hover:bg-white/10 min-[900px]:hidden" :aria-label="t('nav.close')" @click="mobileOpen = false"><PhX :size="18" aria-hidden="true" /></button>
      </div>
      <div class="p-2"><RealmSwitcher :collapsed="collapsed" /></div>
      <AppNav :groups="groups" :collapsed="collapsed" @toggle="toggle" />
    </aside>
    <div class="flex min-w-0 flex-1 flex-col">
      <header class="flex h-12 shrink-0 items-center gap-2 border-b border-slate-200 bg-white px-4 dark:border-slate-800 dark:bg-slate-900">
        <button type="button" class="grid size-8 place-items-center rounded-md text-slate-700 hover:bg-slate-100 min-[900px]:hidden dark:text-slate-200 dark:hover:bg-slate-800" :aria-expanded="mobileOpen" aria-controls="side" :aria-label="t('nav.menu')" data-testid="nav-toggle" @click="mobileOpen = true">
          <PhList :size="20" aria-hidden="true" />
        </button>
        <AppBreadcrumb v-if="crumbs" :crumbs="crumbs" class="min-w-0 flex-1" />
        <div v-else class="flex-1" />
        <button type="button" class="flex h-8 items-center gap-2 rounded-md border border-slate-300 px-2 text-[13px] text-slate-500 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800" :aria-label="t('palette.open')" data-testid="palette-open" @click="palette = true">
          <PhMagnifyingGlass :size="15" aria-hidden="true" />
          <span class="hidden w-40 text-left min-[900px]:inline">{{ t('palette.open') }}</span>
          <kbd class="hidden rounded border border-slate-300 px-1 text-[11px] min-[900px]:inline dark:border-slate-700">⌘K</kbd>
        </button>
        <ThemeToggle />
        <AccountMenu />
      </header>
      <main id="main" class="min-h-0 flex-1 overflow-auto">
        <div class="mx-auto max-w-7xl px-6 py-6"><slot /></div>
      </main>
    </div>
  </div>
  <CommandPalette v-model:open="palette" :groups="groups" />
</template>
