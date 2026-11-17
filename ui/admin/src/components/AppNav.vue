<script setup lang="ts">
import { PhSidebarSimple } from '@phosphor-icons/vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { isCurrent, type NavGroup } from '@/lib/nav'
import { useSession } from '@/stores/session'

// The sidebar menu: groups of links, the first without a heading. `collapsed` shows only the
// icons (the label stays as the tooltip and the accessible name).
defineProps<{ groups: NavGroup[]; collapsed: boolean }>()
defineEmits<{ toggle: [] }>()
const { t } = useI18n()
const route = useRoute()
const session = useSession()
</script>

<template>
  <nav :aria-label="t('nav.label')" class="flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
      <section v-for="g in groups" :key="g.id" :aria-label="t(g.label)">
        <h2 v-if="g.id !== 'realm' && !collapsed" class="mt-4 px-2.5 pb-1 text-[11px] font-semibold tracking-wider text-white/40 uppercase">{{ t(g.label) }}</h2>
        <div v-else-if="g.id !== 'realm'" class="side-rule mx-3 my-2 border-t" aria-hidden="true" />
        <ul class="space-y-px">
          <li v-for="i in g.items" :key="i.route">
            <RouterLink
              :to="{ name: i.route, params: { realm: session.realm } }"
              class="side-link"
              :class="collapsed ? 'justify-center px-0' : ''"
              :aria-current="isCurrent(i, route.name) ? 'page' : undefined"
              :title="t(i.label)"
              :data-testid="`nav-${i.route}`"
            >
              <component :is="i.icon" :size="17" aria-hidden="true" class="shrink-0 opacity-80" />
              <span v-if="!collapsed" class="flex-1" data-testid="nav-label">{{ t(i.label) }}</span>
              <span v-else class="sr-only" data-testid="nav-label">{{ t(i.label) }}</span>
            </RouterLink>
          </li>
        </ul>
      </section>
    </div>
    <div class="side-rule hidden border-t p-2 min-[900px]:block">
      <button type="button" class="side-link w-full text-white/60" :class="collapsed ? 'justify-center px-0' : ''" :title="t('nav.collapse')" :aria-label="t('nav.collapse')" :aria-pressed="collapsed" data-testid="nav-collapse" @click="$emit('toggle')">
        <PhSidebarSimple :size="17" aria-hidden="true" />
        <span v-if="!collapsed">{{ t('nav.collapse') }}</span>
      </button>
    </div>
  </nav>
</template>
