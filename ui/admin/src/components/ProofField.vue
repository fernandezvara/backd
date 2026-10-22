<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import type { ProofMethod } from '@/lib/proof'

defineProps<{ method: ProofMethod | ''; codeSent: boolean; intro?: string }>()
const value = defineModel<string>({ required: true })
defineEmits<{ sendCode: [] }>()
const { t } = useI18n()
</script>

<template>
  <div class="space-y-2 rounded-md border border-slate-200 p-3 dark:border-slate-700">
    <p class="text-xs text-slate-600 dark:text-slate-400">{{ intro ?? t('proof.intro') }}</p>
    <TextField v-if="method === 'password'" v-model="value" type="password" :label="t('proof.password')" autocomplete="current-password" required />
    <TextField v-else-if="method === 'totp'" v-model="value" :label="t('proof.totp')" autocomplete="one-time-code" inputmode="numeric" required />
    <TextField v-else-if="method === 'recovery_code'" v-model="value" :label="t('proof.recovery')" autocomplete="off" required />
    <template v-else-if="method === 'email_code'">
      <AppButton type="button" variant="secondary" @click="$emit('sendCode')">{{ codeSent ? t('proof.resend') : t('proof.send') }}</AppButton>
      <TextField v-model="value" :label="t('proof.emailCode')" autocomplete="one-time-code" inputmode="numeric" required />
    </template>
  </div>
</template>
