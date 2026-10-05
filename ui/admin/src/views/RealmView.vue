<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import LocaleSwitch from '@/components/LocaleSwitch.vue'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import { validRealm } from '@/lib/realm'

const { t } = useI18n()
const router = useRouter()
const realm = ref('')
const invalid = ref(false)

function open() {
  const name = realm.value.trim().toLowerCase()
  invalid.value = !validRealm(name)
  if (!invalid.value) void router.push({ name: 'signin', params: { realm: name } })
}
</script>

<template>
  <main class="mx-auto max-w-sm space-y-6 px-4 py-16">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold">{{ t('realm.choose') }}</h1>
      <div class="flex gap-2">
        <LocaleSwitch />
        <ThemeSwitch />
      </div>
    </div>
    <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('realm.chooseHelp') }}</p>
    <form class="space-y-4" @submit.prevent="open">
      <TextField v-model="realm" :label="t('realm.label')" autocomplete="off" required :error="invalid ? t('realm.invalid') : undefined" />
      <AppButton type="submit">{{ t('realm.open') }}</AppButton>
    </form>
  </main>
</template>
