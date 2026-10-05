import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { validRealm } from '@/lib/realm'
import { useSession, type Area } from '@/stores/session'

declare module 'vue-router' {
  interface RouteMeta {
    /** Needs an admin session in the realm of the URL. */
    auth?: boolean
    /** The admin area the page belongs to: whoami must let the level read it. */
    area?: Area
  }
}

export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'realm', component: () => import('@/views/RealmView.vue') },
  { path: '/r/:realm/signin', name: 'signin', component: () => import('@/views/SignInView.vue'), props: true },
  { path: '/r/:realm/users', name: 'users', component: () => import('@/views/UsersView.vue'), props: true, meta: { auth: true, area: 'users' as const } },
  { path: '/r/:realm/users/:id', name: 'user', component: () => import('@/views/UserView.vue'), props: true, meta: { auth: true, area: 'users' as const } },
  { path: '/r/:realm/invitations', name: 'invitations', component: () => import('@/views/InvitationsView.vue'), props: true, meta: { auth: true, area: 'invitations' } },
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
    if (await session.restore(realm)) {
      // A level that can't read the area gets the overview, not a page of refusals.
      if (to.meta.area && !session.canRead(to.meta.area)) return { name: 'home', params: { realm } }
      return true
    }
    return { name: 'signin', params: { realm }, query: { next: to.fullPath } }
  })
  return router
}
