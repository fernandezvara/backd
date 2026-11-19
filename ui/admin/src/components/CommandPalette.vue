<script setup lang="ts">
import { PhArrowBendDownRight, PhDatabase, PhFunction, PhMagnifyingGlass } from '@phosphor-icons/vue'
import { DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter, type RouteLocationRaw } from 'vue-router'
import type { Component } from 'vue'
import type { NavGroup } from '@/lib/nav'
import { useRealmConfig } from '@/stores/config'
import { useSession } from '@/stores/session'

// ⌘K / Ctrl+K: jump to a page, a collection or a function, or start a common action. What it
// lists comes from what the menus show and from the realm's configuration; nothing is searched
// on the server.
const props = defineProps<{ groups: NavGroup[] }>()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const router = useRouter()
const session = useSession()
const cfg = useRealmConfig()
const query = ref('')
const index = ref(0)
const input = ref<HTMLInputElement>()

interface Entry {
  group: string
  label: string
  icon: Component
  to: RouteLocationRaw
}

type Db = { collections?: { name: string }[]; functions?: { name: string }[] }
const entries = computed<Entry[]>(() => {
  const realm = session.realm
  const out: Entry[] = props.groups.flatMap((g) => g.items.map((i) => ({ group: t('palette.pages'), label: t(i.label), icon: i.icon, to: { name: i.route, params: { realm } } })))
  const dbs = (cfg.config?.databases ?? {}) as Record<string, Db>
  for (const [db, d] of Object.entries(dbs)) {
    for (const c of d.collections ?? []) out.push({ group: t('palette.collections'), label: `${db} / ${c.name}`, icon: PhDatabase, to: { name: 'data-collection', params: { realm, database: db, collection: c.name } } })
    for (const f of d.functions ?? []) out.push({ group: t('palette.functions'), label: `${db} / ${f.name}`, icon: PhFunction, to: { name: 'functions', params: { realm } } })
  }
  if (session.canRead('users')) out.push({ group: t('palette.actions'), label: t('palette.actionList.breach'), icon: PhArrowBendDownRight, to: { name: 'security', params: { realm } } })
  if (session.canRead('data')) out.push({ group: t('palette.actions'), label: t('palette.actionList.backup'), icon: PhArrowBendDownRight, to: { name: 'config', params: { realm }, hash: '#backup' } })
  return out
})
const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  return entries.value.filter((e) => !q || `${e.group} ${e.label}`.toLowerCase().includes(q)).slice(0, 14)
})
watch(query, () => (index.value = 0))
watch(open, async (v) => {
  if (!v) return
  query.value = ''
  index.value = 0
  void cfg.load()
  await nextTick()
  input.value?.focus()
})

async function choose(e: Entry | undefined) {
  if (!e) return
  open.value = false
  await router.push(e.to)
}
function key(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') index.value = Math.min(index.value + 1, shown.value.length - 1)
  else if (e.key === 'ArrowUp') index.value = Math.max(index.value - 1, 0)
  else if (e.key === 'Enter') void choose(shown.value[index.value])
  else return
  e.preventDefault()
}
function global(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    open.value = !open.value
  }
}
onMounted(() => document.addEventListener('keydown', global))
onBeforeUnmount(() => document.removeEventListener('keydown', global))
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-50 bg-slate-950/45" />
      <DialogContent class="fixed top-[12vh] left-1/2 z-50 w-[min(40rem,92vw)] -translate-x-1/2 overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900" :aria-describedby="undefined" data-testid="palette">
        <DialogTitle class="sr-only">{{ t('palette.title') }}</DialogTitle>
        <div class="flex items-center gap-2 border-b border-slate-200 px-3 dark:border-slate-800">
          <PhMagnifyingGlass :size="16" class="text-slate-400" aria-hidden="true" />
          <input ref="input" v-model="query" class="h-11 flex-1 bg-transparent text-[14px] outline-none placeholder:text-slate-400" :placeholder="t('palette.placeholder')" :aria-label="t('palette.placeholder')" @keydown="key" />
        </div>
        <ul class="max-h-80 overflow-y-auto p-1" role="listbox">
          <li v-if="!shown.length" class="px-3 py-6 text-center text-sm text-slate-500">{{ t('palette.empty') }}</li>
          <template v-for="(e, i) in shown" :key="e.group + e.label">
            <li v-if="i === 0 || shown[i - 1].group !== e.group" class="px-2.5 pt-2 pb-1 text-[11px] font-semibold tracking-wider text-slate-500 uppercase" role="presentation">{{ e.group }}</li>
            <li role="option" :aria-selected="i === index">
              <button type="button" class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-[13px]" :class="i === index ? 'bg-brand-50 text-brand-800 dark:bg-brand-900/40 dark:text-brand-100' : ''" @mousemove="index = i" @click="choose(e)">
                <component :is="e.icon" :size="16" aria-hidden="true" class="shrink-0 opacity-70" /> {{ e.label }}
              </button>
            </li>
          </template>
        </ul>
        <p class="border-t border-slate-200 px-3 py-1.5 text-[11px] text-slate-500 dark:border-slate-800">{{ t('palette.hint') }}</p>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
