<script setup lang="ts">
import { PhCaretDown, PhList, PhX } from '@phosphor-icons/vue'
import { PopoverContent, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { isCurrent, type NavGroup } from '@/lib/nav'
import { useSession } from '@/stores/session'

// The grouped menus. Each group is a disclosure (a button that shows its links), the
// pattern for site navigation: the links are plain links, not menu items. Below 900px
// the groups stack in one panel under the hamburger button; `#mobile` holds what the
// shell adds there (the controls that don't fit the bar).
const props = defineProps<{ groups: NavGroup[] }>()
const { t } = useI18n()
const route = useRoute()
const session = useSession()

const open = ref('')
const mobileOpen = ref(false)
const panelId = useId()
const descPrefix = useId()

const currentGroup = computed(() => props.groups.find((g) => g.items.some((i) => isCurrent(i, route.name)))?.id)

function setOpen(id: string, value: boolean) {
  if (value) open.value = id
  else if (open.value === id) open.value = ''
}

// A page opened from a menu closes it.
watch(
  () => route.fullPath,
  () => {
    open.value = ''
    mobileOpen.value = false
  },
)
</script>

<template>
  <nav :aria-label="t('nav.label')" class="flex shrink-0 items-center">
    <button
      type="button"
      class="grid size-8 place-items-center rounded-md text-slate-700 hover:bg-slate-100 min-[900px]:hidden dark:text-slate-200 dark:hover:bg-slate-800"
      :aria-expanded="mobileOpen"
      :aria-controls="panelId"
      :aria-label="mobileOpen ? t('nav.close') : t('nav.menu')"
      data-testid="nav-toggle"
      @click="mobileOpen = !mobileOpen"
    >
      <PhX v-if="mobileOpen" :size="20" aria-hidden="true" />
      <PhList v-else :size="20" aria-hidden="true" />
    </button>

    <ul class="hidden items-stretch gap-0.5 min-[900px]:flex">
      <li v-for="g in groups" :key="g.id" class="flex">
        <PopoverRoot :open="open === g.id" @update:open="(v: boolean) => setOpen(g.id, v)">
          <PopoverTrigger
            class="flex h-14 items-center gap-1 border-b-2 px-3 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
            :class="currentGroup === g.id ? 'border-brand-700 font-semibold dark:border-brand-400' : 'border-transparent text-slate-600 dark:text-slate-300'"
            :data-testid="`nav-group-${g.id}`"
          >
            {{ t(g.label) }}
            <PhCaretDown :size="12" aria-hidden="true" />
          </PopoverTrigger>
          <PopoverContent align="start" :side-offset="0" class="z-50 w-80 rounded-md border border-slate-200 bg-white p-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
            <ul>
              <li v-for="i in g.items" :key="i.route">
                <RouterLink
                  :to="{ name: i.route, params: { realm: session.realm } }"
                  class="flex gap-3 rounded-md border-l-2 px-2.5 py-2 hover:bg-slate-100 dark:hover:bg-slate-800"
                  :class="isCurrent(i, route.name) ? 'border-brand-700 bg-slate-100 dark:border-brand-400 dark:bg-slate-800' : 'border-transparent'"
                  :aria-current="isCurrent(i, route.name) ? 'page' : undefined"
                  :aria-describedby="`${descPrefix}-${i.route}`"
                >
                  <component :is="i.icon" :size="20" aria-hidden="true" class="mt-0.5 shrink-0 text-brand-700 dark:text-brand-400" />
                  <span>
                    <span class="block text-sm font-semibold" data-testid="nav-label">{{ t(i.label) }}</span>
                    <span :id="`${descPrefix}-${i.route}`" aria-hidden="true" class="block text-xs leading-tight text-slate-600 dark:text-slate-400">{{ t(i.desc) }}</span>
                  </span>
                </RouterLink>
              </li>
            </ul>
          </PopoverContent>
        </PopoverRoot>
      </li>
    </ul>

    <div
      v-if="mobileOpen"
      :id="panelId"
      class="absolute inset-x-0 top-14 z-50 max-h-[calc(100vh-3.5rem)] overflow-y-auto border-b border-slate-200 bg-white p-3 shadow-lg min-[900px]:hidden dark:border-slate-800 dark:bg-slate-900"
    >
      <section v-for="g in groups" :key="g.id" class="mb-3" :aria-labelledby="`${panelId}-${g.id}`">
        <h2 :id="`${panelId}-${g.id}`" class="px-2 text-xs font-semibold tracking-wide text-slate-600 uppercase dark:text-slate-400">{{ t(g.label) }}</h2>
        <ul>
          <li v-for="i in g.items" :key="i.route">
            <RouterLink
              :to="{ name: i.route, params: { realm: session.realm } }"
              class="flex items-center gap-3 rounded-md border-l-2 px-2.5 py-2 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
              :class="isCurrent(i, route.name) ? 'border-brand-700 bg-slate-100 font-semibold dark:border-brand-400 dark:bg-slate-800' : 'border-transparent'"
              :aria-current="isCurrent(i, route.name) ? 'page' : undefined"
            >
              <component :is="i.icon" :size="20" aria-hidden="true" class="shrink-0 text-brand-700 dark:text-brand-400" />
              {{ t(i.label) }}
            </RouterLink>
          </li>
        </ul>
      </section>
      <slot name="mobile" />
    </div>
  </nav>
</template>
