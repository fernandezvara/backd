<script setup lang="ts">
import type { PushSend, PushStatus } from 'backd-js'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppAlert from '@/components/AppAlert.vue'
import AppButton from '@/components/AppButton.vue'
import FormDialog from '@/components/FormDialog.vue'
import TextField from '@/components/TextField.vue'
import { useAdmin } from '@/lib/admin'
import { errorText, formatDate, prettyDuration } from '@/lib/format'
import { deviceTotal, sendState, senderLabel } from '@/lib/push'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'

defineProps<{ realm: string }>()
const { t } = useI18n()
const admin = useAdmin()
const session = useSession()
const toasts = useToasts()

const status = ref<PushStatus>()
const statusError = ref('')
const sends = ref<PushSend[]>([])
const sendsError = ref('')
const hasMore = ref(false)
const skip = ref(0)
const pageSize = 25
const filter = ref({ kind: '', fn: '' })
const open = ref<PushSend>()
const loading = ref(false)

async function loadStatus() {
  statusError.value = ''
  try {
    status.value = await admin.push.status()
  } catch (e) {
    statusError.value = errorText(e, t('push.statusFailed'))
  }
}

async function loadSends() {
  if (!status.value?.enabled) return
  loading.value = true
  sendsError.value = ''
  try {
    const page = await admin.push.sends({ kind: filter.value.kind.trim() || undefined, function: filter.value.fn.trim() || undefined, limit: pageSize, skip: skip.value })
    sends.value = page.items
    hasMore.value = page.has_more
  } catch (e) {
    sendsError.value = errorText(e, t('push.sendsFailed'))
  } finally {
    loading.value = false
  }
}
function applyFilter() {
  skip.value = 0
  void loadSends()
}
function page(delta: number) {
  skip.value = Math.max(0, skip.value + delta * pageSize)
  void loadSends()
}
async function show(s: PushSend) {
  try {
    open.value = await admin.push.send(s.id)
  } catch (e) {
    toasts.push(errorText(e, t('common.unexpected')), 'error')
  }
}

// The test send: one user, a raw message or a template kind.
const canTest = computed(() => session.canWrite('users'))
const testing = ref(false)
const test = ref({ email: '', kind: '', title: '', body: '' })
const testError = ref('')
const testBusy = ref(false)
const testResult = ref<{ send_id: string; queued: number; limited: number; no_devices: number }>()
function openTest() {
  test.value = { email: '', kind: '', title: '', body: '' }
  testError.value = ''
  testResult.value = undefined
  testing.value = true
}
async function submitTest() {
  testBusy.value = true
  testError.value = ''
  try {
    const t0 = test.value
    testResult.value = await admin.push.test({
      email: t0.email.trim(),
      ...(t0.kind.trim() ? { kind: t0.kind.trim() } : { title: t0.title, body: t0.body }),
    })
    await loadSends()
  } catch (e) {
    testError.value = errorText(e, t('common.unexpected'))
  } finally {
    testBusy.value = false
  }
}

onMounted(async () => {
  await loadStatus()
  await loadSends()
})
</script>

<template>
  <div class="flex flex-wrap items-baseline justify-between gap-2">
    <h1 class="text-2xl font-semibold">{{ t('push.title') }}</h1>
    <div class="flex items-center gap-2">
      <AppButton variant="secondary" :busy="loading" @click="loadStatus().then(loadSends)">{{ t('push.refresh') }}</AppButton>
      <AppButton v-if="canTest && status?.enabled" @click="openTest">{{ t('push.test.open') }}</AppButton>
    </div>
  </div>

  <AppAlert v-if="statusError" kind="error" class="mt-4">{{ statusError }}</AppAlert>
  <AppAlert v-else-if="status && !status.enabled" class="mt-4" data-testid="push-none">{{ t('push.none') }}</AppAlert>

  <template v-else-if="status">
    <AppAlert v-if="status.missing_secrets?.length" kind="error" class="mt-4" data-testid="push-missing">
      {{ t('push.missing', { names: status.missing_secrets.join(', ') }) }}
    </AppAlert>

    <section class="mt-6" aria-labelledby="push-config-h">
      <h2 id="push-config-h" class="text-lg font-semibold">{{ t('push.config') }}</h2>
      <dl class="mt-2 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4" data-testid="push-config">
        <div v-for="p in status.platforms" :key="p.platform" class="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
          <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ p.platform }}</dt>
          <dd class="mt-1 font-mono">{{ p.provider }}</dd>
        </div>
        <div class="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
          <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('push.limits') }}</dt>
          <dd class="mt-1">{{ t('push.limitsValue', { minute: status.limits?.per_minute, day: status.limits?.per_day }) }}</dd>
        </div>
        <div class="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
          <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('push.devices') }}</dt>
          <dd class="mt-1">{{ t('push.devicesValue', { max: status.devices?.max_per_user, stale: prettyDuration(status.devices?.stale_after ?? '') }) }}</dd>
        </div>
        <div class="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
          <dt class="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">{{ t('push.retention') }}</dt>
          <dd class="mt-1">{{ prettyDuration(status.send_retention ?? '') }}</dd>
        </div>
      </dl>
    </section>

    <section class="mt-8" aria-labelledby="push-sends-h">
      <h2 id="push-sends-h" class="text-lg font-semibold">{{ t('push.sends') }}</h2>
      <form class="mt-2 flex flex-wrap items-end gap-2" @submit.prevent="applyFilter">
        <TextField v-model="filter.kind" :label="t('push.filter.kind')" autocomplete="off" />
        <TextField v-model="filter.fn" :label="t('push.filter.function')" autocomplete="off" />
        <AppButton type="submit" variant="secondary">{{ t('push.filter.apply') }}</AppButton>
      </form>
      <AppAlert v-if="sendsError" kind="error" class="mt-2">{{ sendsError }}</AppAlert>
      <p v-else-if="!sends.length" class="mt-2 text-sm text-slate-600 dark:text-slate-400" data-testid="push-no-sends">{{ t('push.noSends') }}</p>
      <div v-else class="mt-2 overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <table class="w-full text-left text-sm" data-testid="push-sends">
          <thead class="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-600 dark:border-slate-800 dark:text-slate-400">
            <tr>
              <th scope="col" class="px-3 py-2">{{ t('push.col.created') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('push.col.kind') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('push.col.sender') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('push.col.users') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('push.col.devices') }}</th>
              <th scope="col" class="px-3 py-2">{{ t('push.col.state') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in sends" :key="s.id" class="cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800" @click="show(s)">
              <td class="px-3 py-2">{{ formatDate(s.created_at) }}</td>
              <td class="px-3 py-2 font-mono text-xs">{{ s.kind }}</td>
              <td class="px-3 py-2">{{ senderLabel(s.sender) }}</td>
              <td class="px-3 py-2">{{ t('push.usersValue', { queued: s.queued, limited: s.limited, none: s.no_devices }) }}</td>
              <td class="px-3 py-2">{{ t('push.devicesCell', { sent: s.sent, invalid: s.invalid, failed: s.failed }) }}</td>
              <td class="px-3 py-2">{{ t(`push.state.${sendState(s)}`) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="mt-3 flex gap-2">
        <AppButton variant="secondary" :disabled="skip === 0" @click="page(-1)">{{ t('common.previous') }}</AppButton>
        <AppButton variant="secondary" :disabled="!hasMore" @click="page(1)">{{ t('common.next') }}</AppButton>
      </div>
    </section>

    <section v-if="open" class="mt-8 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" aria-labelledby="push-send-h" data-testid="push-send">
      <div class="flex items-baseline justify-between gap-2">
        <h2 id="push-send-h" class="text-lg font-semibold">{{ t('push.detail') }} <span class="font-mono text-sm">{{ open.id }}</span></h2>
        <button type="button" class="underline text-sm" @click="open = undefined">{{ t('common.close') }}</button>
      </div>
      <dl class="mt-3 grid gap-1 text-sm sm:grid-cols-[12rem_1fr]">
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.kind') }}</dt><dd>{{ open.kind }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.sender') }}</dt><dd>{{ senderLabel(open.sender) }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.created') }}</dt><dd>{{ formatDate(open.created_at) }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.state') }}</dt><dd>{{ t(`push.state.${sendState(open)}`) }}<span v-if="open.completed_at"> · {{ formatDate(open.completed_at) }}</span></dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.work') }}</dt>
        <dd>{{ t('push.workValue', { done: open.batches_done, batches: open.batches, unitsDone: open.units_done, units: open.units }) }}</dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.users') }}</dt>
        <dd>{{ t('push.usersValue', { queued: open.queued, limited: open.limited, none: open.no_devices }) }}<span v-if="open.users !== null"> · {{ t('push.named', { n: open.users }) }}</span></dd>
        <dt class="text-slate-600 dark:text-slate-400">{{ t('push.col.devices') }}</dt>
        <dd>{{ t('push.devicesCell', { sent: open.sent, invalid: open.invalid, failed: open.failed }) }} · {{ deviceTotal(open) }}</dd>
      </dl>
      <p class="mt-3 text-xs text-slate-600 dark:text-slate-400">{{ t('push.noContent') }}</p>
    </section>

    <FormDialog v-model:open="testing" :title="t('push.test.title')" :submit="t('push.test.send')" :busy="testBusy" :error="testError" @submit="submitTest">
      <p class="text-sm text-slate-600 dark:text-slate-400">{{ t('push.test.help') }}</p>
      <TextField v-model="test.email" type="email" :label="t('push.test.email')" autocomplete="off" required />
      <TextField v-model="test.kind" :label="t('push.test.kind')" :help="t('push.test.kindHelp')" autocomplete="off" />
      <template v-if="!test.kind.trim()">
        <TextField v-model="test.title" :label="t('push.test.titleField')" autocomplete="off" required />
        <TextField v-model="test.body" :label="t('push.test.bodyField')" autocomplete="off" required />
      </template>
      <p v-if="testResult" class="text-sm" role="status" data-testid="push-test-result">
        {{ t('push.test.result', { id: testResult.send_id, queued: testResult.queued, limited: testResult.limited, none: testResult.no_devices }) }}
      </p>
    </FormDialog>
  </template>
</template>
