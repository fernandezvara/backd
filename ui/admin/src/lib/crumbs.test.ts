import { describe, expect, test } from 'vitest'
import { routes } from '@/router'
import { AREAS } from '@/stores/session'
import { crumbsFor, EXTRA, type CrumbContext } from './crumbs'
import { visibleNav } from './nav'

const groups = visibleNav({ canRead: (a) => (AREAS as readonly string[]).includes(a), orgs: true, storage: true, push: true })
const ctx: CrumbContext = { t: (k) => k, realm: 'acme', groups, name: (route, id) => (route === 'user' ? `${id}@example.com` : undefined) }
const params = { realm: 'acme', id: 'u1', database: 'main', collection: 'notes' }

// Every page that opens inside the shell is a page of the menus or has its parts here, so a new route can't show an empty bar.
const shellRoutes = routes.filter((r) => r.meta?.auth && typeof r.name === 'string')

describe('breadcrumb', () => {
  test('every page of the shell has a breadcrumb that starts at the realm and ends at the page', () => {
    expect(shellRoutes.length).toBeGreaterThan(20)
    for (const r of shellRoutes) {
      const crumbs = crumbsFor(r.name as string, params, ctx)
      expect(crumbs, `route ${String(r.name)}`).not.toBeNull()
      expect(crumbs![0]).toMatchObject({ label: 'acme', to: { name: 'home' } })
      expect(crumbs!.at(-1)!.to, `the last part of ${String(r.name)} is the page itself`).toBeUndefined()
      expect(crumbs!.slice(0, -1).filter((c) => c.to).length).toBeGreaterThanOrEqual(2)
    }
  })

  test('a deep path leads to each page of its parts', () => {
    const crumbs = crumbsFor('data-doc', params, ctx)!
    expect(crumbs.map((c) => c.label)).toEqual(['acme', 'nav.groups.data', 'nav.data', 'main', 'notes', 'u1'])
    expect(crumbs[2].to).toEqual({ name: 'data', params: { realm: 'acme' } })
    expect(crumbs[3].to).toBeUndefined() // a database has no page of its own
    expect(crumbs[4].to).toEqual({ name: 'data-collection', params: { realm: 'acme', database: 'main', collection: 'notes' } })
  })

  test('a user is named by email once the page has it, by id until then', () => {
    expect(crumbsFor('user', params, ctx)!.at(-1)!.label).toBe('u1@example.com')
    expect(crumbsFor('user', params, { ...ctx, name: () => undefined })!.at(-1)!.label).toBe('u1')
  })

  test('the tabs of an area sit below the area, and the group leads to its first page', () => {
    const c = crumbsFor('functions-history', params, ctx)!
    expect(c.map((p) => p.label)).toEqual(['acme', 'nav.groups.compute', 'nav.jobs', 'functions.tabs.history'])
    expect(c[1].to).toEqual({ name: 'functions', params: { realm: 'acme' } })
  })

  test('a route that is not a page of the menus has none', () => {
    expect(crumbsFor('signin', params, ctx)).toBeNull()
    expect(Object.keys(EXTRA).every((n) => shellRoutes.some((r) => r.name === n))).toBe(true)
  })
})
