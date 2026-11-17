<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import AppCard from '@/components/AppCard.vue'
import FormDialog from '@/components/FormDialog.vue'
import ProofField from '@/components/ProofField.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useProof } from '@/lib/proof'
import { useSession } from '@/stores/session'

// Field keys: the keys that seal encrypted fields. A key is created once and rotated when needed; the
// generated value is shown a single time. Both need full administration and a fresh proof.
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const proof = useProof()
const full = computed(() => session.access?.level === 'full' && session.canWrite('secrets'))

const mode = ref<'create' | 'rotate' | ''>('')
const name = ref('')
const own = ref(false)
const value = ref('')
const busy = ref(false)
const error = ref('')
const shown = ref<{ name: string; key: string; fingerprint: string } | null>(null)

function open(m: 'create' | 'rotate') {
  mode.value = m
  name.value = ''
  own.value = false
  value.value = ''
  error.value = ''
  void proof.prepare()
}
async function submit() {
  error.value = ''
  if (!/^[A-Z0-9_]+$/.test(name.value.trim())) {
    error.value = t('fieldKeys.nameHelp')
    return
  }
  busy.value = true
  try {
    await proof.prove()
    const input = own.value && value.value ? { value: value.value } : {}
    const r = mode.value === 'create' ? await admin.secrets.createKey(name.value.trim(), input) : await admin.secrets.rotateKey(name.value.trim(), input)
    shown.value = r.key ? { name: r.name, key: r.key, fingerprint: r.fingerprint } : null
    mode.value = ''
  } catch (e) {
    error.value = errorText(e, t('common.unexpected'))
  } finally {
    value.value = ''
    busy.value = false
  }
}
</script>

<template>
  <AppCard>
    <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('fieldKeys.help') }}</p>
    <div class="mt-3 flex flex-wrap gap-2">
      <AppButton :disabled="!full" @click="open('create')">{{ t('fieldKeys.create') }}</AppButton>
      <AppButton variant="secondary" :disabled="!full" @click="open('rotate')">{{ t('fieldKeys.rotate') }}</AppButton>
    </div>
    <p v-if="!full" class="mt-2 text-xs text-slate-500">{{ t('fieldKeys.needsFull') }}</p>
  </AppCard>

  <AppAlert v-if="shown" class="mt-4" data-testid="new-key">
    <p class="font-medium">{{ t('fieldKeys.shown', { name: shown.name }) }}</p>
    <p class="mt-1 font-mono text-xs break-all select-all">{{ shown.key }}</p>
    <p class="mt-1 text-xs">{{ t('fieldKeys.fingerprint', { fingerprint: shown.fingerprint }) }}</p>
    <AppButton variant="secondary" class="mt-2" @click="shown = null">{{ t('common.dismiss') }}</AppButton>
  </AppAlert>

  <FormDialog :open="!!mode" :title="t(mode === 'rotate' ? 'fieldKeys.rotateTitle' : 'fieldKeys.createTitle')" :description="t(mode === 'rotate' ? 'fieldKeys.rotateBody' : 'fieldKeys.createBody')" :submit="t(mode === 'rotate' ? 'fieldKeys.rotate' : 'fieldKeys.create')" :busy="busy" :error="error" @update:open="(v: boolean) => !v && (mode = '')" @submit="submit">
    <TextField v-model="name" :label="t('fieldKeys.name')" :help="t('fieldKeys.nameHelp')" autocomplete="off" required />
    <label class="flex items-center gap-2 text-sm"><input v-model="own" type="checkbox" /> {{ t('fieldKeys.own') }}</label>
    <TextField v-if="own" v-model="value" type="password" :label="t('fieldKeys.value')" autocomplete="off" />
    <ProofField v-if="proof.needed.value" v-model="proof.value.value" :intro="t('proof.introAction')" :method="proof.method.value" :code-sent="proof.codeSent.value" @send-code="proof.sendCode" />
  </FormDialog>
</template>
