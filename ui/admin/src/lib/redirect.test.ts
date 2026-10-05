import { expect, test } from 'vitest'
import { safeNext } from './redirect'

test('stays inside the realm', () => {
  expect(safeNext('/r/acme/users', 'acme')).toBe('/r/acme/users')
  expect(safeNext('/r/acme', 'acme')).toBe('/r/acme')
  expect(safeNext('/r/acme/data/main/posts?x=1#y', 'acme')).toBe('/r/acme/data/main/posts?x=1#y')
})

test('anything else goes home', () => {
  for (const bad of ['/r/acmeX/users', '/r/other/users', '//evil.example/r/acme', 'https://evil.example', 'javascript:alert(1)', '/r/acme/../../x', '/r/acme/./x', '/r/acme\\..\\x', '/r/acme/\n', undefined, ['/r/acme'], 42]) {
    expect(safeNext(bad, 'acme')).toBe('/r/acme')
  }
})
