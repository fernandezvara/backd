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
  { path: '/r/:realm/apikeys', name: 'apikeys', component: () => import('@/views/ApiKeysView.vue'), props: true, meta: { auth: true, area: 'apikeys' as const } },
  { path: '/r/:realm/secrets', name: 'secrets', component: () => import('@/views/SecretsView.vue'), props: true, meta: { auth: true, area: 'secrets' as const } },
  { path: '/r/:realm/audit', name: 'audit', component: () => import('@/views/AuditView.vue'), props: true, meta: { auth: true, area: 'audit' as const } },
  { path: '/r/:realm/functions', name: 'functions', component: () => import('@/views/FunctionsView.vue'), props: true, meta: { auth: true, area: 'functions' as const } },
  { path: '/r/:realm/functions/history', name: 'functions-history', component: () => import('@/views/InvocationsView.vue'), props: true, meta: { auth: true, area: 'functions' as const } },
  { path: '/r/:realm/functions/jobs', name: 'functions-jobs', component: () => import('@/views/JobsView.vue'), props: true, meta: { auth: true, area: 'functions' as const } },
  { path: '/r/:realm/config', name: 'config', component: () => import('@/views/ConfigOverviewView.vue'), props: true, meta: { auth: true, area: 'config' as const } },
  { path: '/r/:realm/config/settings', name: 'config-settings', component: () => import('@/views/ConfigSettingsView.vue'), props: true, meta: { auth: true, area: 'config' as const } },
  { path: '/r/:realm/config/collections', name: 'config-collections', component: () => import('@/views/ConfigCollectionsView.vue'), props: true, meta: { auth: true, area: 'config' as const } },
  { path: '/r/:realm/config/templates', name: 'config-templates', component: () => import('@/views/ConfigTemplatesView.vue'), props: true, meta: { auth: true, area: 'config' as const } },
  { path: '/r/:realm/data', name: 'data', component: () => import('@/views/DataView.vue'), props: true, meta: { auth: true, area: 'data' as const } },
  { path: '/r/:realm/data/checks', name: 'data-checks', component: () => import('@/views/DataChecksView.vue'), props: true, meta: { auth: true, area: 'data' as const } },
  { path: '/r/:realm/data/:database/:collection', name: 'data-collection', component: () => import('@/views/CollectionView.vue'), props: true, meta: { auth: true, area: 'data' as const } },
  { path: '/r/:realm/data/:database/:collection/new', name: 'data-new', component: () => import('@/views/DocumentView.vue'), props: true, meta: { auth: true, area: 'data' as const } },
  { path: '/r/:realm/data/:database/:collection/:id', name: 'data-doc', component: () => import('@/views/DocumentView.vue'), props: true, meta: { auth: true, area: 'data' as const } },
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
