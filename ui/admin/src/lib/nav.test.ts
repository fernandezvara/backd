import { describe, expect, test } from 'vitest'
import { AREAS, type Area } from '@/stores/session'
import { isCurrent, NAV, visibleNav, type NavContext } from './nav'

const ctx = (read: readonly Area[], over: Partial<NavContext> = {}): NavContext => ({ canRead: (a) => read.includes(a), orgs: true, storage: true, push: true, ...over })
const routes = (c: NavContext) => visibleNav(c).flatMap((g) => g.items.map((i) => i.route))

describe('navigation', () => {
  test('a full level sees every page', () => {
    expect(routes(ctx(AREAS))).toEqual(['home', 'users', 'orgs', 'invitations', 'apikeys', 'secrets', 'data', 'storage', 'functions', 'push', 'functions-jobs', 'audit', 'config'])
  })

  test('a level that reads nothing sees the overview only, in one group', () => {
    const groups = visibleNav(ctx([]))
    expect(groups.map((g) => g.id)).toEqual(['identity'])
    expect(routes(ctx([]))).toEqual(['home'])
  })

  test('a read-only operations level (configuration and functions)', () => {
    expect(routes(ctx(['config', 'functions']))).toEqual(['home', 'storage', 'functions', 'push', 'functions-jobs', 'config'])
  })

  test('organizations, storage and push show only when the realm has them', () => {
    expect(routes(ctx(AREAS, { orgs: false, storage: false, push: false }))).toEqual(['home', 'users', 'invitations', 'apikeys', 'secrets', 'data', 'functions', 'functions-jobs', 'audit', 'config'])
  })

  test('organizations need the users area, storage the configuration, push the functions', () => {
    expect(routes(ctx(['data']))).toEqual(['home', 'data'])
    expect(routes(ctx(['functions'], { storage: true }))).not.toContain('storage')
    expect(routes(ctx(['config']))).not.toContain('push')
  })

  test('a group with nothing to show is dropped', () => {
    expect(visibleNav(ctx(['audit'])).map((g) => g.id)).toEqual(['identity', 'platform'])
  })

  test('each route is in one item, and the sub-pages belong to their item', () => {
    const all = NAV.flatMap((g) => g.items)
    const names = all.flatMap((i) => [i.route, ...(i.also ?? [])])
    expect(new Set(names).size).toBe(names.length)
    const of = (route: string) => all.find((i) => isCurrent(i, route))?.route
    expect(of('user')).toBe('users')
    expect(of('data-doc')).toBe('data')
    expect(of('functions-history')).toBe('functions-jobs')
    expect(of('config-templates')).toBe('config')
    expect(of('signin')).toBeUndefined()
  })
})
