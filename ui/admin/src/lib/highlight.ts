import js from '@speed-highlight/core/languages/js.js'
import jsdoc from '@speed-highlight/core/languages/jsdoc.js'
import json from '@speed-highlight/core/languages/json.js'
import md from '@speed-highlight/core/languages/md.js'
import regex from '@speed-highlight/core/languages/regex.js'
import todo from '@speed-highlight/core/languages/todo.js'
import ts from '@speed-highlight/core/languages/ts.js'
import yaml from '@speed-highlight/core/languages/yaml.js'
import { tokenizeWith } from '@speed-highlight/core/tokenize'

/** A piece of source with the kind of thing it is (`kwd`, `str`, `cmnt`…); null for plain text. */
export interface Token {
  type: string | null
  text: string
}

// The languages the source viewer needs, with the ones they embed (comments, regular expressions).
const languages = { js, jsdoc, json, md, regex, todo, ts, yaml }
const byName: Record<string, keyof typeof languages> = { typescript: 'ts', javascript: 'js', json: 'json', yaml: 'yaml', markdown: 'md' }

/** Above this the source is shown plain: colouring a huge file would only slow the page. */
const MAX_HIGHLIGHT = 200_000

/**
 * Splits source into coloured pieces, one run of text per token, so the page renders them as
 * elements and never as HTML. Unknown languages and very large files come back as one plain token.
 */
export function highlight(src: string, language: string): Token[] {
  const name = byName[language]
  if (!name || src.length > MAX_HIGHLIGHT) return [{ type: null, text: src }]
  const out: Token[] = []
  try {
    tokenizeWith(src, languages[name], (text, type) => {
      if (text) out.push({ type: type ?? null, text })
    }, { languages })
  } catch {
    return [{ type: null, text: src }]
  }
  return out
}
