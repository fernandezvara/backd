<script setup lang="ts">
import { PhCopy, PhDownloadSimple, PhFileCode, PhFileTs } from '@phosphor-icons/vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import SourceViewer from '@/components/SourceViewer.vue'
import { useAdmin } from '@/lib/admin'
import { useRawAdmin } from '@/lib/adminRaw'
import { downloadText } from '@/lib/draft'
import { errorText } from '@/lib/format'
import { queryFields } from '@/lib/query'
import type { Schema } from '@/lib/schema'
import { useRealmConfig } from '@/stores/config'
import { useToasts } from '@/stores/toasts'

// How to read and write this collection: snippets for each client and the generated documents. The
// snippets use a field of the collection's own schema, so they run as they are.
const props = defineProps<{ realm: string; database: string; collection: string }>()
const { t } = useI18n()
const admin = useAdmin()
const raw = useRawAdmin()
const cfg = useRealmConfig()
const toasts = useToasts()
const origin = window.location.origin
const base = `${origin}/v1/${props.realm}`

// An enum field gives a filter that returns something real; without one the example only pages.
const example = computed(() => {
  const f = queryFields(cfg.collectionInfo(props.database, props.collection)?.schema as Schema | undefined).find((x) => x.type === 'enum' && x.options?.length)
  return f ? { field: f.path, value: String(f.options![0]) } : null
})

const LANGS = [
  { id: 'curl', label: 'curl', language: 'text' },
  { id: 'js', label: 'JavaScript', language: 'js' },
  { id: 'python', label: 'Python', language: 'text' },
  { id: 'go', label: 'Go', language: 'text' },
  { id: 'dart', label: 'Dart', language: 'text' },
]
const lang = ref('curl')
const snippets = computed<Record<string, string>>(() => {
  const { database: db, collection: c } = props
  const e = example.value
  const where = e ? JSON.stringify({ [e.field]: e.value }) : ''
  return {
    curl: `# list${e ? ` ${e.value}` : ''} documents, newest first
curl -G -H "Authorization: Bearer $TOKEN" \\
${e ? `  --data-urlencode 'where=${where}' \\\n` : ''}  --data-urlencode 'order_by=-_meta.created_at' \\
  --data-urlencode 'limit=25' \\
  "${base}/${db}/${c}"

# create
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{}' \\
  "${base}/${db}/${c}"
`,
    js: `import { createClient } from 'backd-js'

const backd = createClient({ url: '${origin}', realm: '${props.realm}' })
const page = await backd.db('${db}').collection('${c}').list({
${e ? `  where: { ${e.field}: ${JSON.stringify(e.value)} },\n` : ''}  orderBy: '-_meta.created_at',
  limit: 25,
})
`,
    python: `from backd import Client

with Client(url="${origin}", realm="${props.realm}") as backd:
    page = backd.db("${db}").collection("${c}").list(${e ? `where={"${e.field}": "${e.value}"}, ` : ''}order_by="-_meta.created_at", limit=25)
`,
    go: `c, err := client.New(client.Options{URL: "${origin}", Realm: "${props.realm}"})
page, err := c.DB("${db}").Collection("${c}").List(ctx, client.Query{
${e ? `    Where:   client.Where{"${e.field}": "${e.value}"},\n` : ''}    OrderBy: []string{"-_meta.created_at"},
    Limit:   25,
})
`,
    dart: `final backd = BackdClient(url: '${origin}', realm: '${props.realm}');
final page = await backd.db('${db}').collection('${c}').list(
${e ? `  where: {'${e.field}': '${e.value}'},\n` : ''}  orderBy: '-_meta.created_at',
  limit: 25,
);
`,
  }
})
const file = computed(() => ({ language: LANGS.find((l) => l.id === lang.value)!.language, content: snippets.value[lang.value], truncated: false, binary: false }))

const openapi = `/v1/${props.realm}/_openapi.json`
const busy = ref('')
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push(t('common.copied'))
  } catch {
    toasts.push(t('common.unexpected'), 'error')
  }
}
async function download(kind: 'openapi' | 'types') {
  busy.value = kind
  try {
    if (kind === 'types') downloadText(`${props.realm}.d.ts`, (await raw<{ types: string }>('GET', ['types'], { query: { lang: 'ts' } })).types)
    else downloadText(`${props.realm}-openapi.json`, JSON.stringify(await admin.clientOpenapi(), null, 2), 'application/json')
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="grid gap-5 xl:grid-cols-3" data-testid="collection-api">
    <AppCard flush class="xl:col-span-2">
      <div class="flex flex-wrap items-center gap-1 border-b border-slate-200 px-4 py-2 dark:border-slate-800">
        <h3 class="mr-auto text-[13px] font-semibold">{{ t('data.api.snippets') }}</h3>
        <div role="tablist" :aria-label="t('data.api.snippets')" class="flex gap-1">
          <button
            v-for="l in LANGS"
            :key="l.id"
            type="button"
            role="tab"
            class="rounded-md px-2.5 py-1 text-xs"
            :class="lang === l.id ? 'bg-slate-200 font-semibold dark:bg-slate-700' : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800'"
            :aria-selected="lang === l.id"
            @click="lang = l.id"
          >{{ l.label }}</button>
        </div>
      </div>
      <SourceViewer :name="`${collection}.${lang}`" :file="file" />
    </AppCard>
    <AppCard flush class="h-fit">
      <h3 class="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold dark:border-slate-800">{{ t('data.api.reference') }}</h3>
      <ul class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
        <li class="flex items-center gap-3 p-3">
          <PhFileCode :size="20" class="text-slate-400" aria-hidden="true" />
          <div class="min-w-0 flex-1"><div class="font-medium">{{ t('data.api.openapi') }}</div><div class="truncate font-mono text-xs text-slate-500">{{ openapi }}</div></div>
          <AppButton variant="secondary" :aria-label="t('config.api.copyUrl')" @click="copy(origin + openapi)"><PhCopy :size="14" aria-hidden="true" /></AppButton>
          <AppButton variant="secondary" :busy="busy === 'openapi'" :aria-label="t('config.api.download')" @click="download('openapi')"><PhDownloadSimple :size="14" aria-hidden="true" /></AppButton>
        </li>
        <li class="flex items-center gap-3 p-3">
          <PhFileTs :size="20" class="text-slate-400" aria-hidden="true" />
          <div class="min-w-0 flex-1"><div class="font-medium">{{ t('data.api.types') }}</div><div class="font-mono text-xs text-slate-500">backd types --realm {{ realm }}</div></div>
          <AppButton variant="secondary" :busy="busy === 'types'" @click="download('types')"><PhDownloadSimple :size="14" aria-hidden="true" /> .d.ts</AppButton>
        </li>
      </ul>
    </AppCard>
  </div>
</template>
