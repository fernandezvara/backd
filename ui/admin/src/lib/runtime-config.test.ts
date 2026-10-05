import { expect, test } from 'vitest'
import { DEFAULT_IDLE_SECONDS, loadRuntimeConfig } from './runtime-config'

const answer = (body: unknown, ok = true) => (async () => ({ ok, json: async () => body })) as unknown as typeof fetch

test('takes the idle timeout from the server', async () => {
  expect(await loadRuntimeConfig(answer({ idle_seconds: 90 }))).toEqual({ idleSeconds: 90 })
})

test('falls back to the default on anything unusable', async () => {
  for (const f of [answer({}), answer({ idle_seconds: 'soon' }), answer({ idle_seconds: 0 }), answer({}, false), (async () => { throw new Error('offline') }) as unknown as typeof fetch]) {
    expect(await loadRuntimeConfig(f)).toEqual({ idleSeconds: DEFAULT_IDLE_SECONDS })
  }
})
