import type { DataCheckStarted, DataChecksList, JobSummary } from 'backd-js'
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdmin } from '@/lib/admin'
import { errorText } from '@/lib/format'
import { useToasts } from '@/stores/toasts'

/** How often a running check is asked about. */
const POLL_MS = 2000

/**
 * The schema check of a realm: the one running now (at most one), every
 * collection's latest report, and starting and cancelling. It asks the server
 * every couple of seconds while a check runs, and tells onFinished when one ends.
 */
export function useSchemaChecks(onFinished?: () => void) {
  const { t } = useI18n()
  const admin = useAdmin()
  const toasts = useToasts()
  const listing = ref<DataChecksList>()
  const error = ref('')
  const starting = ref(false)
  const lastStarted = ref<DataCheckStarted>()
  let timer: ReturnType<typeof setTimeout> | undefined
  let stopped = false

  const running = computed<JobSummary | null>(() => listing.value?.running ?? null)
  /** The percentage of the collection being read, 0 to 100. */
  const percent = computed(() => Math.max(0, Math.min(100, running.value?.progress?.current ?? 0)))

  async function refresh() {
    const wasRunning = running.value !== null
    try {
      listing.value = await admin.dataChecks.list()
      error.value = ''
    } catch (e) {
      error.value = errorText(e, t('checks.loadFailed'))
    }
    if (wasRunning && running.value === null) onFinished?.()
    clearTimeout(timer)
    if (!stopped && running.value !== null) timer = setTimeout(refresh, POLL_MS)
  }

  /** Starts a check; returns false when it was refused (one is running, or the server said no). */
  async function start(scope: { database?: string; collection?: string }): Promise<boolean> {
    starting.value = true
    try {
      lastStarted.value = await admin.dataChecks.start(scope)
      toasts.push(t('checks.started'))
      await refresh()
      return true
    } catch (e) {
      toasts.push(errorText(e, t('common.unexpected')), 'error')
      await refresh()
      return false
    } finally {
      starting.value = false
    }
  }

  async function cancel() {
    const job = running.value
    if (!job) return
    try {
      await admin.jobs.cancel(job.id)
      toasts.push(t('checks.cancelled'))
    } catch (e) {
      toasts.push(errorText(e, t('common.unexpected')), 'error')
    }
    await refresh()
  }

  onBeforeUnmount(() => {
    stopped = true
    clearTimeout(timer)
  })
  return { listing, running, percent, error, starting, lastStarted, refresh, start, cancel }
}
