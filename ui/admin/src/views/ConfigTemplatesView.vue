<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { useRealmConfig } from '@/stores/config'

defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()

type Kinds = Record<string, string[]>
// A template's language is its file name without the extension (verify_email/en.html → en).
const language = (file: string) => {
  const base = file.split('/').pop() ?? file
  const dot = base.lastIndexOf('.')
  return dot > 0 ? base.slice(0, dot) : base
}
const sections = computed(() => {
  const templates = (cfg.config?.templates ?? {}) as { email?: Kinds; pages?: Kinds }
  return [
    { id: 'email', title: t('config.templates.email'), empty: t('config.templates.noneEmail'), kinds: templates.email ?? {} },
    { id: 'pages', title: t('config.templates.pages'), empty: t('config.templates.nonePages'), kinds: templates.pages ?? {} },
  ].map((s) => ({ ...s, rows: Object.entries(s.kinds).sort(([a], [b]) => a.localeCompare(b)) }))
})
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.templates')">
    <section v-for="s in sections" :key="s.id" class="mb-8" :aria-labelledby="`tpl-${s.id}`">
      <h2 :id="`tpl-${s.id}`" class="text-lg font-semibold">{{ s.title }}</h2>
      <p v-if="!s.rows.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400">{{ s.empty }}</p>
      <div v-else class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <table class="w-full text-left text-sm" :data-testid="`templates-${s.id}`">
          <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
            <tr>
              <th scope="col" class="px-3 py-2">{{ t('config.templates.kind') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('config.templates.languages') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('config.templates.files') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="[kind, files] in s.rows" :key="kind" class="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
              <th scope="row" class="px-3 py-2 font-mono text-xs font-medium">{{ kind }}</th>
              <td class="px-3 py-2">{{ [...new Set(files.map(language))].join(', ') }}</td>
              <td class="px-3 py-2 font-mono text-xs break-all">
                <div v-for="f in files" :key="f">{{ f }}</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </ConfigFrame>
</template>
