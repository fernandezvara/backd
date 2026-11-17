<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppCard from '@/components/AppCard.vue'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { useAdmin } from '@/lib/admin'
import { buildTree, type TreeNode } from '@/lib/fileTree'
import { useRealmConfig } from '@/stores/config'

// The realm at a glance: how its folder is laid out, what is loaded and the warnings the server gave.
defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const cfg = useRealmConfig()

const paths = ref<string[]>([])
onMounted(async () => {
  try {
    paths.value = (await admin.configFiles()).files.map((f) => f.path)
  } catch {
    paths.value = [] // the layout just isn't drawn
  }
})

// The folder as indented lines: the realm, its databases, their collections and what is in them.
// Below that, and in long folders, only a count: the Files tab has everything.
const lines = computed(() => {
  const out: { text: string; depth: number; dir: boolean }[] = []
  const walk = (n: TreeNode, depth: number) => {
    out.push({ text: n.dir ? `${n.name}/` : n.name, depth, dir: n.dir })
    if (!n.dir) return
    if (depth >= 3) {
      if (n.children.length) out[out.length - 1]!.text += `  (${t('config.overview.files', { n: n.count })})`
      return
    }
    const shown = depth === 0 ? n.children : n.children.slice(0, 10)
    shown.forEach((c) => walk(c, depth + 1))
    if (shown.length < n.children.length) out.push({ text: `… ${t('config.overview.more', { n: n.children.length - shown.length })}`, depth: depth + 1, dir: false })
  }
  const root = buildTree(paths.value)
  root.children.forEach((c) => walk(c, 0))
  return out
})

const counts = computed(() => {
  const dbs = Object.values((cfg.config?.databases ?? {}) as Record<string, { collections: unknown[]; functions: unknown[] }>)
  return { databases: dbs.length, collections: dbs.reduce((n, d) => n + d.collections.length, 0), functions: dbs.reduce((n, d) => n + d.functions.length, 0) }
})
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.overview')">
    <template #default="{ config }">
      <div class="grid gap-5 lg:grid-cols-3">
        <section class="lg:col-span-2" aria-labelledby="layout-h">
          <AppCard flush>
            <h2 id="layout-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('config.overview.layout') }}</h2>
            <pre v-if="lines.length" class="max-h-[30rem] overflow-auto px-4 py-3 font-mono text-[12.5px] leading-relaxed" tabindex="0" data-testid="realm-layout"><template v-for="(l, i) in lines" :key="i"><span :class="l.dir ? 'text-slate-900 dark:text-slate-100' : 'text-slate-600 dark:text-slate-400'">{{ '  '.repeat(l.depth) + l.text }}</span>
</template></pre>
            <p v-else class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('config.overview.noLayout') }}</p>
            <p class="border-t border-slate-200 p-3 text-xs dark:border-slate-800">
              <RouterLink :to="{ name: 'config-files', params: { realm } }" class="underline">{{ t('config.overview.browse') }}</RouterLink>
            </p>
          </AppCard>
        </section>

        <section aria-labelledby="status-h">
          <AppCard flush class="h-fit">
            <h2 id="status-h" class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('config.overview.status') }}</h2>
            <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.realm') }}</dt><dd class="font-medium" data-testid="config-realm">{{ config.realm }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.fingerprint') }}</dt><dd><code class="font-mono text-xs" :title="config.fingerprint" data-testid="config-fingerprint">{{ config.fingerprint }}</code></dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.realmFile') }}</dt><dd class="font-mono text-xs">{{ config.file }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.overview.databases') }}</dt><dd>{{ counts.databases }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.tabs.collections') }}</dt><dd>{{ counts.collections }}</dd></div>
              <div class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('nav.functions') }}</dt><dd>{{ counts.functions }}</dd></div>
              <div v-if="paths.length" class="flex justify-between gap-3 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.tabs.files') }}</dt><dd>{{ paths.length }}</dd></div>
            </dl>
            <p class="border-t border-slate-200 p-3 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-400">{{ t('config.fingerprintHelp') }}</p>
          </AppCard>
        </section>
      </div>

      <section class="mt-6" aria-labelledby="warnings-h">
        <h2 id="warnings-h" class="mb-2 text-sm font-semibold">{{ t('config.warnings.title') }}</h2>
        <p v-if="!config.warnings.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.warnings.none') }}</p>
        <ul v-else class="space-y-2" data-testid="config-warnings">
          <li v-for="w in config.warnings" :key="w.code" class="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100">
            <code class="font-mono text-xs">{{ w.code }}</code>
            <span class="ml-2">{{ w.message }}</span>
          </li>
        </ul>
      </section>
    </template>
  </ConfigFrame>
</template>
