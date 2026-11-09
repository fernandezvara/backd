import { describe, expect, it } from 'vitest'
import { configNotLoaded } from './config-loaded'

const answer = (status: number, body: unknown) => (async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch

describe('configNotLoaded', () => {
  it('reads the message of a server whose config has not loaded', async () => {
    const f = answer(503, { error: { code: 'config_not_loaded', message: 'the config has not loaded yet' } })
    expect(await configNotLoaded(f, 'http://x')).toBe('the config has not loaded yet')
  })
  it('is null for a ready server, another unavailability, or no answer', async () => {
    expect(await configNotLoaded(answer(200, { status: 'ready' }), 'http://x')).toBeNull()
    expect(await configNotLoaded(answer(503, { error: { code: 'unavailable' } }), 'http://x')).toBeNull()
    const failing = (async () => { throw new TypeError('down') }) as unknown as typeof fetch
    expect(await configNotLoaded(failing, 'http://x')).toBeNull()
  })
})
