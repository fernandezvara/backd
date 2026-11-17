<script setup lang="ts">
import { PhCaretDown, PhCaretRight, PhFile, PhFolder } from '@phosphor-icons/vue'
import { computed, ref, watch } from 'vue'
import { buildTree, visibleRows } from '@/lib/fileTree'

// A folder tree of paths: folders open and close, a file is chosen. A filter text opens everything
// that matches. `first` folders are open to start with.
const props = defineProps<{ paths: string[]; label: string; filter?: string; openDepth?: number }>()
const selected = defineModel<string>({ required: true })

const filtered = computed(() => {
  const q = (props.filter ?? '').trim().toLowerCase()
  return q ? props.paths.filter((p) => p.toLowerCase().includes(q)) : props.paths
})
const tree = computed(() => buildTree(filtered.value))
const open = ref(new Set<string>())

// Open the first levels (and whatever the selected file sits in); a filter opens all.
function reset() {
  const next = new Set<string>()
  const q = (props.filter ?? '').trim()
  const walk = (n: ReturnType<typeof buildTree>, depth: number) => {
    for (const c of n.children) {
      if (c.dir) {
        if (q || depth < (props.openDepth ?? 1)) next.add(c.path)
        walk(c, depth + 1)
      }
    }
  }
  walk(tree.value, 0)
  const parts = selected.value.split('/')
  for (let i = 1; i < parts.length; i++) next.add(parts.slice(0, i).join('/'))
  open.value = next
}
watch(() => [props.paths, props.filter], reset, { immediate: true })

const rows = computed(() => visibleRows(tree.value, open.value))
function toggle(path: string) {
  const next = new Set(open.value)
  if (!next.delete(path)) next.add(path)
  open.value = next
}
</script>

<template>
  <nav :aria-label="label" class="p-2" data-testid="file-tree">
    <p v-if="!rows.length" class="px-2 py-3 text-xs text-slate-500">—</p>
    <template v-for="r in rows" :key="r.node.path">
      <button
        v-if="r.node.dir"
        type="button"
        class="flex h-7 w-full items-center gap-1.5 rounded-md pr-2 text-left text-[12.5px] hover:bg-slate-100 dark:hover:bg-slate-800"
        :style="{ paddingLeft: `${r.depth * 12 + 4}px` }"
        :aria-expanded="open.has(r.node.path)"
        @click="toggle(r.node.path)"
      >
        <component :is="open.has(r.node.path) ? PhCaretDown : PhCaretRight" :size="11" class="shrink-0 text-slate-400" aria-hidden="true" />
        <PhFolder :size="14" class="shrink-0 text-slate-400" aria-hidden="true" />
        <span class="truncate font-mono">{{ r.node.name }}</span>
        <span class="ml-auto text-[11px] text-slate-400">{{ r.node.count }}</span>
      </button>
      <button
        v-else
        type="button"
        class="flex h-7 w-full items-center gap-1.5 rounded-md pr-2 text-left font-mono text-[12.5px] hover:bg-slate-100 dark:hover:bg-slate-800"
        :class="r.node.path === selected ? 'bg-slate-100 font-semibold dark:bg-slate-800' : ''"
        :style="{ paddingLeft: `${r.depth * 12 + 20}px` }"
        :aria-current="r.node.path === selected ? 'true' : undefined"
        @click="selected = r.node.path"
      >
        <PhFile :size="14" class="shrink-0 text-slate-400" aria-hidden="true" />
        <span class="truncate">{{ r.node.name }}</span>
      </button>
    </template>
  </nav>
</template>
