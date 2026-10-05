import { createRouter, createWebHistory } from 'vue-router'
import { validRealm } from '@/lib/realm'
import { useSession } from '@/stores/session'

declare module 'vue-router' {
  interface RouteMeta {
    /** Needs an admin session in the realm of the URL. */
    auth?: boolean
  }
}

export const routes = [
  { path: '/', name: 'realm', component: () => import('@/views/RealmView.vue') },
  { path: '/r/:realm/signin', name: 'signin', component: () => import('@/views/SignInView.vue'), props: true },
  { path: '/r/:realm', name: 'home', component: () => import('@/views/HomeView.vue'), props: true, meta: { auth: true } },
  { path: '/:rest(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue') },
]

export function makeRouter(history = createWebHistory(import.meta.env.BASE_URL)) {
  const router = createRouter({ history, routes })
  router.beforeEach(async (to) => {
    const realm = typeof to.params.realm === 'string' ? to.params.realm : ''
    if (realm && !validRealm(realm)) return { name: 'not-found', params: { rest: to.path.slice(1).split('/') }, replace: true }
    if (!to.meta.auth) return true
    const session = useSession()
    if (session.realm !== realm && session.signedIn) await session.signOut()
    if (await session.restore(realm)) return true
    return { name: 'signin', params: { realm }, query: { next: to.fullPath } }
  })
  return router
}
