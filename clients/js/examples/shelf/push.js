#!/usr/bin/env node
// push.js — a fake asset feed: signs a payload the way a real provider
// would (x-signature: sha256=<hmac-hex> of the raw body) and POSTs it to the
// shelf's import webhook. Chapter 11 of the tutorial.
//
//   node push.js http://localhost:8080 <secret> "Title" https://example.com "note"
// or with no arguments, a demo event against the local stack (secret is
// read from $IMPORT_WEBHOOK_SECRET or defaults to dev-secret).
import { createHmac } from 'node:crypto'

const [base, secret, title, url, kind] = process.argv.slice(2)
const BASE = base ?? 'http://localhost:8080'
const SECRET = secret ?? process.env.IMPORT_WEBHOOK_SECRET ?? 'dev-secret'

const event = {
  event_id: `feed-${Date.now()}`,
  title: title ?? 'The backd handbook',
  kind: kind ?? 'link',
  url: url ?? 'https://fernandezvara.github.io/backd/',
  tags: ['feed', 'imported'],
}
const body = JSON.stringify(event)
const signature = 'sha256=' + createHmac('sha256', SECRET).update(body).digest('hex')

const res = await fetch(`${BASE}/v1/shelf/main/_func/import`, {
  method: 'POST',
  headers: { 'content-type': 'application/json', 'x-signature': signature },
  body,
})
console.log(`${res.status} ${await res.text()}`)
