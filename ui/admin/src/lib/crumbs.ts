import type { RouteLocationRaw } from 'vue-router'
import { isCurrent, type NavGroup } from './nav'

export interface Crumb {
  label: string
  /** Where the part leads; the page you are on and a part with no page of its own have none. */
  to?: RouteLocationRaw
}

type Params = Record<string, string | string[] | undefined>

export interface CrumbContext {
  /** i18n lookup. */
  t: (key: string) => string
  /** The realm of the session. */
  realm: string
  /** What the menus show to this account: the group and page of the route come from here. */
  groups: NavGroup[]
  /** A name the page learned (an email, an organization's name), if it has loaded. */
  name: (route: string, id: string) => string | undefined
}

const str = (v: string | string[] | undefined) => (Array.isArray(v) ? v.join('/') : (v ?? ''))

/**
 * The parts of the route below its page in the menus. A route that is its page in the menus
 * (Users, Audit...) has none; the ones that are deeper or one of several tabs of an area have.
 * Every page of the shell that is not a page of the menus must be here: the test walks the router.
 */
export const EXTRA: Record<string, (c: CrumbContext, p: Params) => Crumb[]> = {
  user: (c, p) => [{ label: c.name('user', str(p.id)) ?? str(p.id) }],
  org: (c, p) => [{ label: c.name('org', str(p.id)) ?? str(p.id) }],
  'data-checks': (c) => [{ label: c.t('checks.title') }],
  'data-collection': (_c, p) => [{ label: str(p.database) }, { label: str(p.collection) }],
  'data-new': (c, p) => [{ label: str(p.database) }, collectionCrumb(c, p), { label: c.t('data.collection.new') }],
  'data-doc': (c, p) => [{ label: str(p.database) }, collectionCrumb(c, p), { label: str(p.id) }],
  'functions-history': (c) => [{ label: c.t('functions.tabs.history') }],
  'config-settings': (c) => [{ label: c.t('config.tabs.settings') }],
  'config-collections': (c) => [{ label: c.t('config.tabs.collections') }],
  'config-templates': (c) => [{ label: c.t('config.tabs.templates') }],
}

function collectionCrumb(c: CrumbContext, p: Params): Crumb {
  return { label: str(p.collection), to: { name: 'data-collection', params: { realm: c.realm, database: str(p.database), collection: str(p.collection) } } }
}

/**
 * The breadcrumb of a route: the realm, the group and the page in the menus, then what is below
 * the page. Every part with a page of its own leads there. Null for a route that is not a page
 * of the menus (the sign-in page, not found).
 */
export function crumbsFor(name: string, params: Params, c: CrumbContext): Crumb[] | null {
  const group = c.groups.find((g) => g.items.some((i) => isCurrent(i, name)))
  const item = group?.items.find((i) => isCurrent(i, name))
  if (!group || !item) return null
  const extra = EXTRA[name]?.(c, params) ?? []
  const here = (route: string) => ({ name: route, params: { realm: c.realm } })
  const parts: Crumb[] = [
    { label: c.realm, to: here('home') },
    // A group is not a page: it leads to its first page, which is where its menu starts.
    { label: c.t(group.label), to: here(group.items[0].route) },
    { label: c.t(item.label), to: here(item.route) },
    ...extra,
  ]
  // The page you are on is not a link, and the pieces that have no page of their own were never links.
  const last = parts.length - 1
  return parts.map((part, i) => (i === last ? { label: part.label } : part))
}
