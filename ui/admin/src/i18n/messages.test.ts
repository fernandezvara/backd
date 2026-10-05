import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test } from 'vitest'
import { detectLocale, resolveLocale } from './locale'

type Tree = { [key: string]: string | Tree }

function flatten(tree: Tree, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(tree)) {
    if (typeof value === 'string') out[prefix + key] = value
    else Object.assign(out, flatten(value, `${prefix}${key}.`))
  }
  return out
}

// The files as written: importing them would give the build's compiled messages.
const load = (name: string) => flatten(JSON.parse(readFileSync(resolve(process.cwd(), 'src/i18n', `${name}.json`), 'utf8')) as Tree)
const english = load('en')
const spanish = load('es')
const placeholders = (text: string) => [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()

// Values that are the same in both languages: names, technical words, symbols.
const SAME = new Set([
  'app.name', 'common.no', 'common.allRealm', 'realm.label', 'realm.current', 'users.roles', 'user.id', 'user.roles.title',
  'invitations.create.tokenLabel', 'audit.actor', 'functions.modes.sync', 'functions.modes.async', 'history.actor', 'history.ms',
  'jobs.noResult', 'config.realm', 'config.roles.title', 'data.collection.json', 'data.doc.tabs.json', 'data.doc.id', 'data.doc.problem',
])

describe('Spanish messages', () => {
  test('every English key has a Spanish one, and the other way round', () => {
    expect(Object.keys(spanish).filter((k) => !(k in english)), 'keys only in es.json').toEqual([])
    expect(Object.keys(english).filter((k) => !(k in spanish)), 'keys missing from es.json').toEqual([])
  })

  test('each message uses the same placeholders as its English original', () => {
    for (const [key, text] of Object.entries(english)) {
      expect(placeholders(spanish[key] ?? ''), key).toEqual(placeholders(text))
    }
  })

  test('nothing is empty, and nothing was left in English by accident', () => {
    for (const [key, text] of Object.entries(spanish)) {
      expect(text.trim(), key).not.toBe('')
      if (text === english[key]) expect(SAME.has(key), `${key} is identical to English: translate it, or add it to SAME`).toBe(true)
    }
  })

  test('markup never travels in a message', () => {
    for (const [key, text] of Object.entries({ ...english, ...spanish })) expect(text, key).not.toMatch(/[<>]/)
  })
})

describe('choosing the language', () => {
  test('the first browser language with a message file wins', () => {
    expect(detectLocale(['es-ES', 'en-US'])).toBe('es')
    expect(detectLocale(['fr-FR', 'es-MX', 'en'])).toBe('es')
    expect(detectLocale(['en-GB'])).toBe('en')
    expect(detectLocale(['fr', 'de'])).toBe('en')
    expect(detectLocale([])).toBe('en')
  })

  test('a choice beats the browser', () => {
    expect(resolveLocale('en', ['es'])).toBe('en')
    expect(resolveLocale('es', ['en'])).toBe('es')
    expect(resolveLocale('auto', ['es'])).toBe('es')
  })
})
