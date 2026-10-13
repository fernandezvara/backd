<script setup lang="ts">
import { AuthenticationError, NetworkError } from 'backd-js'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import LocaleSwitch from '@/components/LocaleSwitch.vue'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import { safeNext } from '@/lib/redirect'
import { NotAdminError, useSession } from '@/stores/session'

const props = defineProps<{ realm: string }>()
const { t } = useI18n()
const session = useSession()
const router = useRouter()
const route = useRoute()

const email = ref('')
const password = ref('')
const keep = ref(false)
const busy = ref(false)
const error = ref('')
// The second step: a code, or setting an app up (then the recovery codes, shown once).
const code = ref('')
const setup = ref<{ setup_id: string; secret: string; qr: string } | null>(null)
const recovery = ref<string[]>([])

// Only the realm's own pages are followed after signing in.
const next = () => safeNext(route.query.next, props.realm)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const result = await session.signIn(props.realm, email.value.trim(), password.value, keep.value)
    password.value = ''
    if (result === 'done') await router.replace(next())
  } catch (e) {
    error.value =
      e instanceof NotAdminError ? t('signIn.notAdmin') : e instanceof NetworkError ? t('signIn.unreachable') : e instanceof AuthenticationError ? t('signIn.failed') : t('signIn.failed')
  } finally {
    busy.value = false
  }
}

async function verify() {
  busy.value = true
  error.value = ''
  try {
    await session.verifyCode(code.value)
    code.value = ''
    await router.replace(next())
  } catch (e) {
    error.value = e instanceof NotAdminError ? t('signIn.notAdmin') : t('signIn.codeFailed')
  } finally {
    busy.value = false
  }
}

async function beginSetup() {
  busy.value = true
  error.value = ''
  try {
    setup.value = await session.startEnrollment()
  } catch {
    error.value = t('signIn.unreachable')
  } finally {
    busy.value = false
  }
}

async function confirmSetup() {
  busy.value = true
  error.value = ''
  try {
    recovery.value = await session.confirmEnrollment(setup.value!.setup_id, code.value)
    code.value = ''
    setup.value = null
  } catch (e) {
    error.value = e instanceof NotAdminError ? t('signIn.notAdmin') : t('signIn.codeFailed')
  } finally {
    busy.value = false
  }
}

function cancel() {
  session.cancelStep()
  code.value = ''
  setup.value = null
  error.value = ''
}
</script>

<template>
  <main class="mx-auto max-w-sm space-y-6 px-4 py-16">
    <div class="flex items-center justify-between">
      <span class="font-semibold">{{ t('app.name') }}</span>
      <div class="flex gap-2">
        <LocaleSwitch />
        <ThemeSwitch />
      </div>
    </div>
    <div class="rounded-md bg-brand-50 px-3 py-2 text-brand-800 dark:bg-brand-800 dark:text-brand-50" data-testid="realm-badge">
      <span class="text-xs uppercase tracking-wide">{{ t('realm.label') }}</span>
      <p class="text-lg font-semibold">{{ realm }}</p>
    </div>
    <h1 class="text-xl font-semibold">{{ t('signIn.title', { realm }) }}</h1>
    <AppAlert v-if="session.ended">{{ t(`signIn.ended.${session.ended}`) }}</AppAlert>
    <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
    <section v-if="recovery.length" class="space-y-3" aria-labelledby="recovery-h">
      <h2 id="recovery-h" class="text-lg font-semibold">{{ t('signIn.recovery.title') }}</h2>
      <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('signIn.recovery.body') }}</p>
      <ul class="grid grid-cols-2 gap-1 font-mono text-sm" data-testid="recovery-codes">
        <li v-for="c in recovery" :key="c">{{ c }}</li>
      </ul>
      <AppButton @click="router.replace(next())">{{ t('signIn.recovery.continue') }}</AppButton>
    </section>
    <form v-else-if="session.step?.kind === 'code'" class="space-y-4" @submit.prevent="verify">
      <p class="text-sm">{{ t('signIn.code.intro') }}</p>
      <TextField v-model="code" :label="t('signIn.code.label')" autocomplete="one-time-code" required />
      <div class="flex gap-2">
        <AppButton type="submit" :busy="busy">{{ t('signIn.code.submit') }}</AppButton>
        <AppButton type="button" variant="secondary" @click="cancel">{{ t('signIn.cancel') }}</AppButton>
      </div>
    </form>
    <section v-else-if="session.step?.kind === 'enroll'" class="space-y-4">
      <p class="text-sm">{{ t('signIn.enroll.intro') }}</p>
      <AppButton v-if="!setup" :busy="busy" @click="beginSetup">{{ t('signIn.enroll.start') }}</AppButton>
      <form v-else class="space-y-4" @submit.prevent="confirmSetup">
        <img :src="setup.qr" :alt="t('signIn.enroll.qrAlt')" width="180" height="180" />
        <p class="text-xs text-slate-600 dark:text-slate-400">{{ t('signIn.enroll.secret') }} <code class="break-all">{{ setup.secret }}</code></p>
        <TextField v-model="code" :label="t('signIn.enroll.label')" autocomplete="one-time-code" required />
        <AppButton type="submit" :busy="busy">{{ t('signIn.enroll.submit') }}</AppButton>
      </form>
      <AppButton type="button" variant="secondary" @click="cancel">{{ t('signIn.cancel') }}</AppButton>
    </section>
    <form v-else class="space-y-4" @submit.prevent="submit">
      <TextField v-model="email" type="email" :label="t('signIn.email')" autocomplete="username" required />
      <TextField v-model="password" type="password" :label="t('signIn.password')" autocomplete="current-password" required />
      <div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="keep" type="checkbox" />
          {{ t('signIn.keep') }}
        </label>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('signIn.keepHelp') }}</p>
      </div>
      <AppButton type="submit" :busy="busy">{{ busy ? t('signIn.submitting') : t('signIn.submit') }}</AppButton>
    </form>
    <RouterLink :to="{ name: 'realm' }" class="text-sm underline">{{ t('signIn.otherRealm') }}</RouterLink>
  </main>
</template>
