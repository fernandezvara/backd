import { PhBell, PhBuildings, PhClockCounterClockwise, PhDatabase, PhEnvelopeSimple, PhFunction, PhHardDrives, PhKey, PhLockKey, PhScroll, PhSlidersHorizontal, PhSquaresFour, PhUsers } from '@phosphor-icons/vue'
import type { Component } from 'vue'
import type { Area } from '@/stores/session'

/** What decides whether an item shows: the level's read access and what the realm has switched on. */
export interface NavContext {
  canRead: (area: Area) => boolean
  /** The realm has organizations (also true when the level can't read the configuration: the server decides). */
  orgs: boolean
  /** `storage:` is configured. */
  storage: boolean
  /** `push:` is configured. */
  push: boolean
}

export interface NavItem {
  /** The route the item opens. */
  route: string
  /** i18n key of the label, and of the one-line description (`nav.desc.*`). */
  label: string
  desc: string
  icon: Component
  /** Routes (besides `route`) that count as being in the item. */
  also?: string[]
  show: (c: NavContext) => boolean
}

export interface NavGroup {
  id: 'identity' | 'access' | 'data' | 'compute' | 'platform'
  /** i18n key of the group's name. */
  label: string
  items: NavItem[]
}

/** The navigation of the admin UI: every page a person can open from the menus, in its group. */
export const NAV: NavGroup[] = [
  {
    id: 'identity',
    label: 'nav.groups.identity',
    items: [
      { route: 'home', label: 'nav.overview', desc: 'nav.desc.overview', icon: PhSquaresFour, show: () => true },
      { route: 'users', label: 'nav.users', desc: 'nav.desc.users', icon: PhUsers, also: ['user'], show: (c) => c.canRead('users') },
      { route: 'orgs', label: 'nav.orgs', desc: 'nav.desc.orgs', icon: PhBuildings, also: ['org'], show: (c) => c.canRead('users') && c.orgs },
      { route: 'invitations', label: 'nav.invitations', desc: 'nav.desc.invitations', icon: PhEnvelopeSimple, show: (c) => c.canRead('invitations') },
    ],
  },
  {
    id: 'access',
    label: 'nav.groups.access',
    items: [
      { route: 'apikeys', label: 'nav.apikeys', desc: 'nav.desc.apikeys', icon: PhKey, show: (c) => c.canRead('apikeys') },
      { route: 'secrets', label: 'nav.secrets', desc: 'nav.desc.secrets', icon: PhLockKey, show: (c) => c.canRead('secrets') },
    ],
  },
  {
    id: 'data',
    label: 'nav.groups.data',
    items: [
      { route: 'data', label: 'nav.data', desc: 'nav.desc.data', icon: PhDatabase, also: ['data-checks', 'data-collection', 'data-new', 'data-doc'], show: (c) => c.canRead('data') },
      { route: 'storage', label: 'nav.storage', desc: 'nav.desc.storage', icon: PhHardDrives, show: (c) => c.canRead('config') && c.storage },
    ],
  },
  {
    id: 'compute',
    label: 'nav.groups.compute',
    items: [
      { route: 'functions', label: 'nav.functions', desc: 'nav.desc.functions', icon: PhFunction, show: (c) => c.canRead('functions') },
      { route: 'push', label: 'nav.push', desc: 'nav.desc.push', icon: PhBell, show: (c) => c.canRead('functions') && c.push },
      { route: 'functions-jobs', label: 'nav.jobs', desc: 'nav.desc.jobs', icon: PhClockCounterClockwise, also: ['functions-history'], show: (c) => c.canRead('functions') },
    ],
  },
  {
    id: 'platform',
    label: 'nav.groups.platform',
    items: [
      { route: 'audit', label: 'nav.audit', desc: 'nav.desc.audit', icon: PhScroll, show: (c) => c.canRead('audit') },
      { route: 'config', label: 'nav.config', desc: 'nav.desc.config', icon: PhSlidersHorizontal, also: ['config-settings', 'config-collections', 'config-templates'], show: (c) => c.canRead('config') },
    ],
  },
]

/** The groups with the items this context shows; a group with none is dropped. */
export function visibleNav(c: NavContext): NavGroup[] {
  return NAV.map((g) => ({ ...g, items: g.items.filter((i) => i.show(c)) })).filter((g) => g.items.length > 0)
}

/** Whether the route belongs to the item. */
export const isCurrent = (item: NavItem, route: string | symbol | null | undefined) => route === item.route || (typeof route === 'string' && !!item.also?.includes(route))
