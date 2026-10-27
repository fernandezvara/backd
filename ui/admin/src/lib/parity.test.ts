import { describe, expect, test } from 'vitest'
import { routes } from '@/router'
import { AREAS, type Area } from '@/stores/session'
import { visibleNav } from './nav'

// Milestone 35 changed how the pages are reached, not which pages there are: these two tests pin
// what the interface before it offered (the routes and the rules of its flat navigation bar).

describe('nothing was lost in the redesign', () => {
  test('every page of the old interface is still a route, with the same address and area', () => {
    const before: [string, string, Area | undefined][] = [
      ['realm', '/', undefined],
      ['signin', '/r/:realm/signin', undefined],
      ['users', '/r/:realm/users', 'users'],
      ['user', '/r/:realm/users/:id', 'users'],
      ['orgs', '/r/:realm/orgs', 'users'],
      ['org', '/r/:realm/orgs/:id', 'users'],
      ['invitations', '/r/:realm/invitations', 'invitations'],
      ['apikeys', '/r/:realm/apikeys', 'apikeys'],
      ['secrets', '/r/:realm/secrets', 'secrets'],
      ['audit', '/r/:realm/audit', 'audit'],
      ['functions', '/r/:realm/functions', 'functions'],
      ['functions-history', '/r/:realm/functions/history', 'functions'],
      ['functions-jobs', '/r/:realm/functions/jobs', 'functions'],
      ['push', '/r/:realm/push', 'functions'],
      ['config', '/r/:realm/config', 'config'],
      ['config-settings', '/r/:realm/config/settings', 'config'],
      ['config-collections', '/r/:realm/config/collections', 'config'],
      ['config-templates', '/r/:realm/config/templates', 'config'],
      ['data', '/r/:realm/data', 'data'],
      ['data-checks', '/r/:realm/data/checks', 'data'],
      ['data-collection', '/r/:realm/data/:database/:collection', 'data'],
      ['data-new', '/r/:realm/data/:database/:collection/new', 'data'],
      ['data-doc', '/r/:realm/data/:database/:collection/:id', 'data'],
      ['storage', '/r/:realm/storage', 'config'],
      ['home', '/r/:realm', undefined],
    ]
    for (const [name, path, area] of before) {
      const r = routes.find((x) => x.name === name)
      expect(r, `route ${name}`).toBeDefined()
      expect(r!.path).toBe(path)
      expect(r!.meta?.area).toBe(area)
    }
  })

  test('for every level and realm setup the menus offer the same pages as the old bar, and the jobs page where functions are readable', () => {
    // The rules of the old AppShell, as they were.
    const oldBar = (read: readonly Area[], orgs: boolean, storage: boolean, push: boolean) => {
      const can = (a: Area) => read.includes(a)
      return [
        ['home', true],
        ['users', can('users')],
        ['orgs', can('users') && orgs],
        ['invitations', can('invitations')],
        ['apikeys', can('apikeys')],
        ['data', can('data')],
        ['storage', can('config') && storage],
        ['functions', can('functions')],
        ['push', can('functions') && push],
        ['secrets', can('secrets')],
        ['audit', can('audit')],
        ['config', can('config')],
      ]
        .filter(([, show]) => show)
        .map(([name]) => name as string)
        .sort()
    }
    let checked = 0
    for (let mask = 0; mask < 1 << AREAS.length; mask++) {
      const read = AREAS.filter((_, i) => mask & (1 << i))
      for (const [orgs, storage, push] of [0, 1, 2, 3, 4, 5, 6, 7].map((n) => [!!(n & 1), !!(n & 2), !!(n & 4)])) {
        const now = visibleNav({ canRead: (a) => read.includes(a), orgs, storage, push }).flatMap((g) => g.items.map((i) => i.route))
        expect(now.filter((r) => r !== 'functions-jobs').sort()).toEqual(oldBar(read, orgs, storage, push))
        expect(now.includes('functions-jobs')).toBe(read.includes('functions'))
        checked++
      }
    }
    expect(checked).toBe(2048)
  })
})
