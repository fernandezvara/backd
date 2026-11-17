<script setup lang="ts">
import { PhCaretUpDown, PhCheck, PhPlus } from '@phosphor-icons/vue'
import { PopoverContent, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { validRealm } from '@/lib/realm'
import { useRealms } from '@/stores/realms'
import { useSession } from '@/stores/session'

// The realm you are in, and the others this browser has signed in to. Opening another one goes to
// its sign-in page (the session of a realm is its own); the list lives in this browser only.
defineProps<{ collapsed: boolean }>()
const { t } = useI18n()
const session = useSession()
const realms = useRealms()
const router = useRouter()
const open = ref(false)
const adding = ref(false)
const name = ref('')
const bad = ref(false)

async function go(realm: string) {
  open.value = false
  if (realm === session.realm) return
  await router.push({ name: 'signin', params: { realm } })
}
async function connect() {
  const r = name.value.trim()
  bad.value = !validRealm(r)
  if (bad.value) return
  name.value = ''
  adding.value = false
  await go(r)
}
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger
      class="flex w-full items-center gap-2 rounded-md border border-white/15 px-2 py-1.5 text-left text-white hover:bg-white/10"
      :class="collapsed ? 'justify-center' : ''"
      :title="t('realm.label') + ': ' + session.realm"
      :aria-label="t('realm.switch', { realm: session.realm })"
      data-testid="realm-badge"
    >
      <span class="grid size-6 shrink-0 place-items-center rounded bg-brand-400 text-xs font-bold text-brand-950" aria-hidden="true">{{ session.realm.charAt(0).toUpperCase() }}</span>
      <span v-if="!collapsed" class="min-w-0 flex-1">
        <span class="block truncate text-[13px] leading-tight font-semibold">{{ session.realm }}</span>
        <span class="block truncate text-[11px] leading-tight text-white/60">{{ t('realm.label') }}</span>
      </span>
      <PhCaretUpDown v-if="!collapsed" :size="14" aria-hidden="true" class="text-white/50" />
    </PopoverTrigger>
    <PopoverContent align="start" :side-offset="6" class="z-50 w-64 rounded-lg border border-slate-200 bg-white p-1 text-slate-900 shadow-xl dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100">
      <p class="px-2.5 py-1.5 text-[11px] font-semibold tracking-wider text-slate-500 uppercase">{{ t('realm.list') }}</p>
      <button v-for="r in [...new Set([session.realm, ...realms.list])]" :key="r" type="button" class="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[13px] hover:bg-slate-100 dark:hover:bg-slate-800" @click="go(r)">
        <span class="grid size-5 place-items-center rounded bg-slate-200 text-[11px] font-bold dark:bg-slate-700" aria-hidden="true">{{ r.charAt(0).toUpperCase() }}</span>
        <span class="flex-1 truncate">{{ r }}</span>
        <PhCheck v-if="r === session.realm" :size="14" class="text-brand-700 dark:text-brand-400" aria-hidden="true" />
      </button>
      <div class="my-1 border-t border-slate-200 dark:border-slate-800" />
      <button v-if="!adding" type="button" class="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[13px] hover:bg-slate-100 dark:hover:bg-slate-800" @click="adding = true">
        <PhPlus :size="14" aria-hidden="true" /> {{ t('realm.connect') }}
      </button>
      <form v-else class="space-y-1 p-2" @submit.prevent="connect">
        <label class="block text-xs font-medium text-slate-500">
          {{ t('realm.name') }}
          <input v-model="name" class="mt-1 block h-8 w-full rounded-md border border-slate-300 bg-white px-2.5 text-[13px] text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100" autocomplete="off" />
        </label>
        <p v-if="bad" class="text-xs text-red-700 dark:text-red-400" role="alert">{{ t('realm.invalid') }}</p>
        <button type="submit" class="h-8 w-full rounded-md bg-brand-700 text-[13px] font-medium text-white hover:bg-brand-800">{{ t('realm.open') }}</button>
      </form>
      <p class="px-2.5 py-1.5 text-[11px] text-slate-500">{{ t('realm.note') }}</p>
    </PopoverContent>
  </PopoverRoot>
</template>
