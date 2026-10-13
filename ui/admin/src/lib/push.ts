import type { PushSend } from 'backd-js'

/** The sends' state as a word: done, planning (an audience still being paged) or delivering. */
export type SendState = 'completed' | 'planning' | 'delivering'

export function sendState(s: Pick<PushSend, 'completed' | 'planned' | 'batches_done' | 'batches'>): SendState {
  if (s.completed) return 'completed'
  if (!s.planned || s.batches_done < s.batches) return 'planning'
  return 'delivering'
}

/** How many devices a send answered for (sent, removed as dead, failed). */
export function deviceTotal(s: Pick<PushSend, 'sent' | 'invalid' | 'failed'>): number {
  return s.sent + s.invalid + s.failed
}

/** A sender as a short label: "function main/notify" → "main/notify", "admin:key:ci" → "admin (key:ci)". */
export function senderLabel(sender: string): string {
  if (sender.startsWith('function:')) return sender.slice('function:'.length)
  if (sender.startsWith('admin:')) return `admin (${sender.slice('admin:'.length)})`
  return sender
}
