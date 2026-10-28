<script setup lang="ts">
import { AuthenticationError, NetworkError } from 'backd-js'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import TextField from '@/components/TextField.vue'
import LocaleSwitch from '@/components/LocaleSwitch.vue'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import { lastRealm, rememberRealm, validRealm } from '@/lib/realm'
import { safeNext } from '@/lib/redirect'
import { NotAdminError, useSession } from '@/stores/session'

// The realm comes from the address when it names one, else from the last sign-in; it is a field.
const props = defineProps<{ realm?: string }>()
const { t } = useI18n()
const session = useSession()
const router = useRouter()
const route = useRoute()

const email = ref('')
const realm = ref(props.realm ?? lastRealm())
const realmInvalid = ref(false)
watch(() => props.realm, (r) => { if (r) realm.value = r })
const password = ref('')
const keep = ref(false)
const busy = ref(false)
const error = ref('')
// The second step: a code, or setting an app up (then the recovery codes, shown once).
const code = ref('')
const setup = ref<{ setup_id: string; secret: string; qr: string } | null>(null)
const recovery = ref<string[]>([])
// The consents of the step, each ticked by the person before they can go on.
const ticked = ref<Record<string, boolean>>({})
const allTicked = () => (session.step?.consents ?? []).every((c) => ticked.value[c.name])

// Only the realm's own pages are followed after signing in.
const next = () => safeNext(route.query.next, realm.value)

async function submit() {
  realm.value = realm.value.trim().toLowerCase()
  realmInvalid.value = !validRealm(realm.value)
  if (realmInvalid.value) return
  busy.value = true
  error.value = ''
  try {
    rememberRealm(realm.value)
    const result = await session.signIn(realm.value, email.value.trim(), password.value, keep.value)
    password.value = ''
    ticked.value = {}
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
    const done = await session.confirmEnrollment(setup.value!.setup_id, code.value)
    recovery.value = done.codes
    code.value = ''
    setup.value = null
  } catch (e) {
    error.value = e instanceof NotAdminError ? t('signIn.notAdmin') : t('signIn.codeFailed')
  } finally {
    busy.value = false
  }
}

// After the recovery codes: the consents that were waiting, or the app.
async function continueAfterRecovery() {
  recovery.value = []
  if (session.step?.kind !== 'consent') await router.replace(next())
}

async function acceptConsents() {
  busy.value = true
  error.value = ''
  try {
    await session.acceptConsents()
    ticked.value = {}
    await router.replace(next())
  } catch (e) {
    error.value = e instanceof NotAdminError ? t('signIn.notAdmin') : t('signIn.consent.failed')
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
    <h1 class="text-xl font-semibold">{{ t('signIn.title') }}</h1>
    <AppAlert v-if="session.ended">{{ t(`signIn.ended.${session.ended}`) }}</AppAlert>
    <AppAlert v-if="error" kind="error">{{ error }}</AppAlert>
    <section v-if="recovery.length" class="space-y-3" aria-labelledby="recovery-h">
      <h2 id="recovery-h" class="text-lg font-semibold">{{ t('signIn.recovery.title') }}</h2>
      <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('signIn.recovery.body') }}</p>
      <ul class="grid grid-cols-2 gap-1 font-mono text-sm" data-testid="recovery-codes">
        <li v-for="c in recovery" :key="c">{{ c }}</li>
      </ul>
      <AppButton @click="continueAfterRecovery">{{ t('signIn.recovery.continue') }}</AppButton>
    </section>
    <form v-else-if="session.step?.kind === 'consent'" class="space-y-4" data-testid="consent-step" @submit.prevent="acceptConsents">
      <p class="text-sm">{{ t('signIn.consent.intro') }}</p>
      <section v-for="c in session.step.consents" :key="c.name" class="space-y-2">
        <h2 class="font-semibold">{{ c.name }} ({{ c.version }})</h2>
        <pre class="max-h-48 overflow-auto whitespace-pre-wrap rounded border border-slate-200 p-2 text-xs dark:border-slate-700">{{ c.text }}</pre>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="ticked[c.name]" type="checkbox" :data-testid="`accept-${c.name}`" />
          {{ t('signIn.consent.accept', { name: c.name }) }}
        </label>
      </section>
      <div class="flex gap-2">
        <AppButton type="submit" :busy="busy" :disabled="!allTicked()">{{ t('signIn.consent.submit') }}</AppButton>
        <AppButton type="button" variant="secondary" @click="cancel">{{ t('signIn.cancel') }}</AppButton>
      </div>
    </form>
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
      <TextField v-model="realm" :label="t('realm.label')" autocomplete="organization" required :error="realmInvalid ? t('realm.invalid') : undefined" data-testid="realm-field" />
      <TextField v-model="password" type="password" :label="t('signIn.password')" autocomplete="current-password" required />
      <div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="keep" type="checkbox" />
          {{ t('signIn.keep') }}
        </label>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ t('signIn.keepHelp') }}</p>
      </div>
      <div class="flex justify-end">
        <AppButton type="submit" :busy="busy">{{ busy ? t('signIn.submitting') : t('signIn.submit') }}</AppButton>
      </div>
    </form>
  </main>
</template>
