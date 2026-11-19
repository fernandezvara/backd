import { afterEach, expect, test, vi } from 'vitest'
import { downloadText } from './download'

afterEach(() => vi.restoreAllMocks())

test('the text is offered as a file with the name given', async () => {
  const created: Blob[] = []
  vi.stubGlobal('URL', { createObjectURL: (b: Blob) => (created.push(b), 'blob:x'), revokeObjectURL: () => {} })
  const clicks: string[] = []
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicks.push(`${this.download} ${this.href}`)
  })
  downloadText('openapi.json', '{"a":1}', 'application/json')
  expect(clicks).toEqual(['openapi.json blob:x'])
  expect(created).toHaveLength(1)
  expect(created[0].type).toBe('application/json;charset=utf-8')
  expect(await created[0].text()).toBe('{"a":1}')
  vi.unstubAllGlobals()
})
