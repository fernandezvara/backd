import type { Admin } from 'backd-js'
import { useSession } from '@/stores/session'

/** The admin API of the signed-in session. */
export function useAdmin(): Admin {
  const client = useSession().client
  if (!client) throw new Error('not signed in')
  return client.admin
}
