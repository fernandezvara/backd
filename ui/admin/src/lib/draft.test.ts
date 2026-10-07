import { describe, expect, it } from 'vitest'
import { draftProblem, rulesFileText, schemaFileText } from './draft'

describe('schemaFileText', () => {
  it('is the schema without the file fields backd injects', () => {
    const text = schemaFileText({ type: 'object', properties: { title: { type: 'string' }, cover: { type: 'object' } }, required: ['title', 'cover'] }, ['cover'])
    expect(JSON.parse(text)).toEqual({ type: 'object', properties: { title: { type: 'string' } }, required: ['title'] })
    expect(text.endsWith('\n')).toBe(true)
  })
  it('leaves a schema without file fields as it is', () => {
    expect(JSON.parse(schemaFileText({ type: 'object', properties: { a: { type: 'string' } } }, []))).toEqual({ type: 'object', properties: { a: { type: 'string' } } })
  })
})

describe('rulesFileText', () => {
  it('writes each key once, in order, quoting what needs it', () => {
    const text = rulesFileText({
      create: { expression: 'user != nil', from: 'write' },
      update: { expression: 'user != nil', from: 'write' },
      delete: { expression: 'user != nil', from: 'write' },
      read: { expression: 'user != nil && document._meta.owner == user.id ', from: 'read' },
    })
    expect(text).toContain('read: "user != nil && document._meta.owner == user.id"\nwrite: "user != nil"\n')
    expect(text.match(/^write:/gm)).toHaveLength(1)
  })
  it('keeps a multi-line expression as a block', () => {
    const text = rulesFileText({ read: { expression: 'a &&\n  b', from: 'read' } })
    expect(text).toContain('read: |-\n  a &&\n    b\n')
  })
})

describe('draftProblem', () => {
  it('checks JSON and trusts YAML to the server', () => {
    expect(draftProblem('json', '{"a": 1}')).toBe('')
    expect(draftProblem('json', '{"a": ')).not.toBe('')
    expect(draftProblem('yaml', 'anything: [')).toBe('')
  })
})
