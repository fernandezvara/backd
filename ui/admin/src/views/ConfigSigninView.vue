<script setup lang="ts">
import {
  PhAppleLogo, PhArrowRight, PhClockCountdown, PhDeviceMobile, PhEnvelopeSimple, PhFingerprint, PhGithubLogo, PhGoogleLogo,
  PhKey, PhLockKey, PhMicrosoftOutlookLogo, PhPassword, PhPlugs, PhShieldCheck, PhUsers,
} from '@phosphor-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppBadge from '@/components/AppBadge.vue'
import AppCard from '@/components/AppCard.vue'
import ConfigFrame from '@/components/ConfigFrame.vue'
import { useRawAdmin } from '@/lib/adminRaw'
import { useRealmConfig } from '@/stores/config'

// How people get into this realm, drawn from the configuration: who may arrive, the ways in (each
// strategy as the steps a person goes through, each provider), what protects the account and what a
// session is like afterwards. Read-only: it is realm.yaml, shown.
defineProps<{ realm: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()
const raw = useRawAdmin()

interface Strategy { name: string; methods: string[]; roles: string[] }
interface Provider { name: string; kind: string; client_id?: string; tenant?: string; issuer?: string; client_secret?: string }
interface Settings {
  auth: string
  signup: string
  strategies: Strategy[]
  providers: Provider[]
  passkeys: { enabled: boolean; rp_id: string; rp_name: string; origins: string[] }
  totp: { enabled: boolean; issuer: string }
  magic_link: { enabled: boolean; signup: boolean }
  sign_in: { app_url: string; allowed_redirects: string[]; reauth_ttl: string }
  sessions: { idle_timeout: string; max_lifetime: string; admin_idle_timeout: string; admin_max_lifetime: string; cookie: { enabled: boolean; same_site: string } }
  password: { min_length: number }
  login_throttle: { account_threshold: number; ip_threshold: number; window: string; max_delay: string }
  account: { require_verified_email: boolean; welcome_email: boolean; allow_email_change: boolean }
  consents: Record<string, { required: boolean; current: string }>
}
const s = computed(() => cfg.config?.settings as unknown as Settings | undefined)

// Who has to use a second factor: the server says, with who still hasn't (that is the Security page).
const mfa = ref<{ enabled: boolean; required_all: boolean; required_roles: string[] }>()
onMounted(async () => {
  try {
    mfa.value = await raw('GET', ['mfa'])
  } catch {
    // A level that can't read it just doesn't see this line.
  }
})

const icons: Record<string, unknown> = { password: PhPassword, totp: PhDeviceMobile, passkey: PhFingerprint, passkeys: PhFingerprint, magic_link: PhEnvelopeSimple, google: PhGoogleLogo, microsoft: PhMicrosoftOutlookLogo, apple: PhAppleLogo, github: PhGithubLogo }
const providerOf = (name: string) => s.value?.providers.find((p) => p.name === name)
const iconOf = (method: string) => icons[method] ?? icons[providerOf(method)?.kind ?? ''] ?? (providerOf(method) ? PhPlugs : PhKey)
const methodLabel = (m: string) => {
  const known = ['password', 'totp', 'passkey', 'magic_link']
  return known.includes(m) ? t(`config.signin.methods.${m}`) : m
}
const registration = computed(() => (s.value?.auth === 'disabled' ? 'off' : (s.value?.signup ?? 'closed')))
const required = computed(() => Object.entries(s.value?.consents ?? {}).filter(([, c]) => c.required))
const optional = computed(() => Object.entries(s.value?.consents ?? {}).filter(([, c]) => !c.required))
const none = (v: string) => v || t('config.settings.empty')
</script>

<template>
  <ConfigFrame :heading="t('config.tabs.signin')">
    <template #default>
      <p v-if="!s" class="text-sm">{{ t('config.loading') }}</p>
      <div v-else-if="s.auth === 'disabled'" class="rounded-lg border border-red-300 bg-red-50 p-4 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200">{{ t('config.signin.authOff') }}</div>
      <div v-else class="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)_16rem]" data-testid="signin-map">
        <!-- Who arrives -->
        <section class="space-y-3" aria-labelledby="who-h">
          <h2 id="who-h" class="text-sm font-semibold">{{ t('config.signin.who') }}</h2>
          <AppCard>
            <div class="flex items-center gap-2"><PhUsers :size="20" class="text-slate-400" aria-hidden="true" /><b>{{ t('config.signin.registration') }}</b></div>
            <p class="mt-2"><AppBadge :kind="registration === 'open' ? 'warn' : 'ok'" data-testid="registration">{{ t(`config.signin.signup.${registration}`) }}</AppBadge></p>
            <p class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ t(`config.signin.signupHelp.${registration}`) }}</p>
            <p v-if="s.account.require_verified_email" class="mt-2 text-xs"><PhShieldCheck :size="13" class="mr-1 inline text-emerald-600" aria-hidden="true" />{{ t('config.signin.verifiedEmail') }}</p>
          </AppCard>
          <AppCard v-if="required.length || optional.length">
            <b class="text-[13px]">{{ t('config.signin.consents') }}</b>
            <ul class="mt-2 space-y-1 text-xs">
              <li v-for="[name, c] in required" :key="name" class="flex items-center justify-between gap-2"><span class="font-mono">{{ name }}</span><AppBadge kind="warn">{{ t('config.signin.mustAccept') }} v{{ c.current }}</AppBadge></li>
              <li v-for="[name, c] in optional" :key="name" class="flex items-center justify-between gap-2"><span class="font-mono">{{ name }}</span><AppBadge>{{ t('config.signin.optional') }} v{{ c.current }}</AppBadge></li>
            </ul>
          </AppCard>
        </section>

        <!-- The ways in -->
        <section class="space-y-3" aria-labelledby="ways-h">
          <h2 id="ways-h" class="text-sm font-semibold">{{ t('config.signin.ways') }}</h2>
          <AppCard v-for="st in s.strategies" :key="st.name" :data-testid="`strategy-${st.name}`">
            <div class="flex flex-wrap items-center gap-2"><b class="font-mono text-[13px]">{{ st.name }}</b><AppBadge v-if="st.roles.length" kind="violet" :title="t('config.signin.rolesHelp')">{{ t('config.signin.rolesOnly', { roles: st.roles.join(', ') }) }}</AppBadge></div>
            <ol class="mt-3 flex flex-wrap items-center gap-2" :aria-label="t('config.signin.steps')">
              <template v-for="(m, i) in st.methods" :key="`${m}-${i}`">
                <li v-if="i > 0" aria-hidden="true"><PhArrowRight :size="14" class="text-slate-400" /></li>
                <li class="inline-flex items-center gap-1.5 rounded-lg border border-slate-200 bg-slate-50 px-2.5 py-1.5 text-[13px] dark:border-slate-700 dark:bg-slate-800">
                  <span class="grid size-5 place-items-center rounded-full bg-brand-700 text-[11px] font-semibold text-white">{{ i + 1 }}</span>
                  <component :is="iconOf(m)" :size="16" class="text-slate-500" aria-hidden="true" />{{ methodLabel(m) }}
                </li>
              </template>
            </ol>
          </AppCard>
          <p v-if="!s.strategies.length" class="text-sm text-slate-600 dark:text-slate-400">{{ t('config.signin.noStrategies') }}</p>

          <div class="grid gap-3 sm:grid-cols-2">
            <AppCard>
              <div class="flex items-center gap-2"><PhFingerprint :size="20" class="text-slate-400" aria-hidden="true" /><b>{{ t('config.signin.methods.passkey') }}</b><AppBadge class="ml-auto" :kind="s.passkeys.enabled ? 'ok' : 'neutral'">{{ s.passkeys.enabled ? t('config.signin.on') : t('config.signin.off') }}</AppBadge></div>
              <p v-if="s.passkeys.enabled" class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ s.passkeys.rp_name || s.passkeys.rp_id }} · <span class="font-mono">{{ s.passkeys.rp_id }}</span></p>
            </AppCard>
            <AppCard>
              <div class="flex items-center gap-2"><PhEnvelopeSimple :size="20" class="text-slate-400" aria-hidden="true" /><b>{{ t('config.signin.methods.magic_link') }}</b><AppBadge class="ml-auto" :kind="s.magic_link.enabled ? 'ok' : 'neutral'">{{ s.magic_link.enabled ? t('config.signin.on') : t('config.signin.off') }}</AppBadge></div>
              <p v-if="s.magic_link.enabled" class="mt-2 text-xs text-slate-600 dark:text-slate-400">{{ s.magic_link.signup ? t('config.signin.magicSignup') : t('config.signin.magicExisting') }}</p>
            </AppCard>
          </div>

          <AppCard flush>
            <h3 class="border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">{{ t('config.signin.providers') }}</h3>
            <p v-if="!s.providers.length" class="p-4 text-[13px] text-slate-600 dark:text-slate-400">{{ t('config.signin.noProviders') }}</p>
            <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800" data-testid="providers">
              <li v-for="p in s.providers" :key="p.name" class="flex items-center gap-3 px-4 py-3 text-[13px]">
                <component :is="iconOf(p.name)" :size="22" class="text-slate-400" aria-hidden="true" />
                <div class="min-w-0 flex-1"><b>{{ p.name }}</b> <span class="text-slate-500">{{ p.kind }}</span>
                  <div class="truncate font-mono text-xs text-slate-600 dark:text-slate-400">{{ p.client_id }}<template v-if="p.tenant"> · {{ p.tenant }}</template><template v-if="p.issuer"> · {{ p.issuer }}</template></div></div>
                <span v-if="p.client_secret" class="font-mono text-[11px] text-slate-500">{{ p.client_secret }}</span>
              </li>
            </ul>
          </AppCard>
        </section>

        <!-- Protection and sessions -->
        <section class="space-y-3" aria-labelledby="prot-h">
          <h2 id="prot-h" class="text-sm font-semibold">{{ t('config.signin.protection') }}</h2>
          <AppCard flush>
            <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400"><PhLockKey :size="13" class="mr-1 inline" aria-hidden="true" />{{ t('config.signin.minLength') }}</dt><dd>{{ s.password.min_length }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.throttle') }}</dt><dd class="text-right">{{ t('config.signin.throttleText', { n: s.login_throttle.account_threshold, window: none(s.login_throttle.window) }) }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.totp') }}</dt><dd>{{ s.totp.enabled ? t('config.signin.on') : t('config.signin.off') }}</dd></div>
              <div v-if="mfa" class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.mfaRequired') }}</dt><dd class="text-right">{{ mfa.required_all ? t('config.signin.everyone') : mfa.required_roles.length ? mfa.required_roles.join(', ') : t('config.signin.nobody') }}</dd></div>
            </dl>
          </AppCard>
          <h2 class="pt-1 text-sm font-semibold"><PhClockCountdown :size="14" class="mr-1 inline" aria-hidden="true" />{{ t('config.signin.sessions') }}</h2>
          <AppCard flush>
            <dl class="divide-y divide-slate-100 text-[13px] dark:divide-slate-800">
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.idle') }}</dt><dd>{{ none(s.sessions.idle_timeout) }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.maxLife') }}</dt><dd>{{ none(s.sessions.max_lifetime) }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.adminIdle') }}</dt><dd>{{ none(s.sessions.admin_idle_timeout) }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.cookie') }}</dt><dd>{{ s.sessions.cookie.enabled ? s.sessions.cookie.same_site : t('config.signin.off') }}</dd></div>
              <div class="flex justify-between gap-2 px-4 py-2"><dt class="text-slate-600 dark:text-slate-400">{{ t('config.signin.reauth') }}</dt><dd>{{ none(s.sign_in.reauth_ttl) }}</dd></div>
            </dl>
          </AppCard>
          <AppCard v-if="s.sign_in.allowed_redirects.length || s.sign_in.app_url">
            <b class="text-[13px]">{{ t('config.signin.redirects') }}</b>
            <p v-if="s.sign_in.app_url" class="mt-1 break-all font-mono text-xs">{{ s.sign_in.app_url }}</p>
            <ul class="mt-1 space-y-0.5 font-mono text-xs text-slate-600 dark:text-slate-400"><li v-for="u in s.sign_in.allowed_redirects" :key="u" class="break-all">{{ u }}</li></ul>
          </AppCard>
        </section>
      </div>
    </template>
  </ConfigFrame>
</template>
