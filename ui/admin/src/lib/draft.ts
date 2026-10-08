// Files the interface prepares for download: a change to the configuration is built
// here, in the browser, and offered as a file to review and commit. Nothing is sent
// to the server, which never has its configuration written through the interface.
import { withoutProperties, type Schema } from './schema'

/**
 * A collection's schema.json as the repository holds it: the configuration shows the
 * schema backd compiles, which includes the file fields it injects from `files:`
 * (they are declared in collection.yaml, and backd refuses them in schema.json).
 */
export function schemaFileText(schema: unknown, fileFields: string[]): string {
  return JSON.stringify(withoutProperties(schema as Schema, fileFields), null, 2) + '\n'
}

export interface RuleView {
  expression: string
  from: string
}

const ORDER = ['read', 'create', 'update', 'delete', 'restore', 'purge', 'write']

/**
 * A `rules:` section rebuilt from the expressions the server runs, one key per rule: the key
 * a rule came from (`write` covers create, update and delete), once. Comments and layout
 * of the original are not known to the server, so they are not kept.
 */
export function rulesSectionText(rules: Record<string, RuleView>): string {
  const byKey = new Map<string, string>()
  for (const rule of Object.values(rules)) if (!byKey.has(rule.from)) byKey.set(rule.from, rule.expression.trim())
  const keys = [...byKey.keys()].sort((a, b) => (ORDER.indexOf(a) + 1 || 99) - (ORDER.indexOf(b) + 1 || 99))
  const lines = ['rules:']
  for (const key of keys) {
    const expr = byKey.get(key)!
    if (expr.includes('\n')) lines.push(`  ${key}: |-`, ...expr.split('\n').map((l) => `    ${l}`))
    else lines.push(`  ${key}: ${JSON.stringify(expr)}`) // a JSON string is a YAML string
  }
  return lines.join('\n') + '\n'
}

/**
 * The text of a collection's collection.yaml to start a change from: the file as written
 * (comments and all) when the server sent it, else a file holding just the rules it runs.
 */
export function collectionFileText(raw: string | undefined, rules: Record<string, RuleView> | undefined): string {
  if (raw?.trim()) return raw
  return rules ? rulesSectionText(rules) : ''
}

/** What is wrong with a draft, or '' (only JSON can be checked here; a rules file is checked at startup). */
export function draftProblem(kind: 'json' | 'yaml', text: string): string {
  if (kind !== 'json') return ''
  try {
    JSON.parse(text)
    return ''
  } catch (e) {
    return e instanceof Error ? e.message : String(e)
  }
}

/** Offers text as a file to save, from the browser. */
export function downloadText(filename: string, text: string, mime = 'text/plain') {
  const url = URL.createObjectURL(new Blob([text], { type: `${mime};charset=utf-8` }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
