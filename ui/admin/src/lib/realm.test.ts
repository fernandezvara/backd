import { expect, test } from 'vitest'
import { validRealm } from './realm'

test('realm names', () => {
  for (const ok of ['acme', 'a', 'acme-eu_1']) expect(validRealm(ok)).toBe(true)
  for (const bad of ['', 'Acme', '-x', 'a b', '../x', 'a/b', 'x'.repeat(64)]) expect(validRealm(bad)).toBe(false)
})
