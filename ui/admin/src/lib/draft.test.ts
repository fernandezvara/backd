import { describe, expect, it } from 'vitest'
import { collectionFileText, draftProblem, rulesSectionText, schemaFileText } from './draft'

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

describe('rulesSectionText', () => {
  it('writes each key once, in order, quoting what needs it', () => {
    const text = rulesSectionText({
      create: { expression: 'user != nil', from: 'write' },
      update: { expression: 'user != nil', from: 'write' },
      delete: { expression: 'user != nil', from: 'write' },
      read: { expression: 'user != nil && document._meta.owner == user.id ', from: 'read' },
    })
    expect(text).toContain('rules:\n  read: "user != nil && document._meta.owner == user.id"\n  write: "user != nil"\n')
    expect(text.match(/^ {2}write:/gm)).toHaveLength(1)
  })
  it('keeps a multi-line expression as a block', () => {
    const text = rulesSectionText({ read: { expression: 'a &&\n  b', from: 'read' } })
    expect(text).toContain('rules:\n  read: |-\n    a &&\n      b\n')
  })
})

describe('collectionFileText', () => {
  it('starts from the file as written, comments and all', () => {
    expect(collectionFileText('# mine\nrules:\n  read: "true"\nsoft_delete: true\n', { read: { expression: 'x', from: 'read' } })).toBe('# mine\nrules:\n  read: "true"\nsoft_delete: true\n')
  })
  it('rebuilds a file with just the rules when there is none', () => {
    expect(collectionFileText(undefined, { read: { expression: 'user != nil', from: 'read' } })).toBe('rules:\n  read: "user != nil"\n')
    expect(collectionFileText('  \n', undefined)).toBe('')
  })
})

describe('draftProblem', () => {
  it('checks JSON and trusts YAML to the server', () => {
    expect(draftProblem('json', '{"a": 1}')).toBe('')
    expect(draftProblem('json', '{"a": ')).not.toBe('')
    expect(draftProblem('yaml', 'anything: [')).toBe('')
  })
})
