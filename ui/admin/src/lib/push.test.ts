import { describe, expect, test } from 'vitest'
import { deviceTotal, sendState, senderLabel } from './push'

describe('push helpers', () => {
  test('the state of a send', () => {
    expect(sendState({ completed: true, planned: true, batches_done: 1, batches: 1 })).toBe('completed')
    expect(sendState({ completed: false, planned: false, batches_done: 0, batches: 0 })).toBe('planning')
    expect(sendState({ completed: false, planned: true, batches_done: 1, batches: 3 })).toBe('planning')
    expect(sendState({ completed: false, planned: true, batches_done: 3, batches: 3 })).toBe('delivering')
  })

  test('devices answered for', () => {
    expect(deviceTotal({ sent: 5, invalid: 2, failed: 1 })).toBe(8)
  })

  test('the sender label', () => {
    expect(senderLabel('function:main/notify')).toBe('main/notify')
    expect(senderLabel('admin:key:ci')).toBe('admin (key:ci)')
    expect(senderLabel('something')).toBe('something')
  })
})
