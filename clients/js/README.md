# @backd/client

JavaScript client for [backd](https://github.com/fernandezvara/backd): sign-up, login and sessions, collections with access rules, and the admin API. Plain ES modules with TypeScript declarations, no dependencies, no build step. Runs in browsers, Node 20+, Deno, Bun and edge runtimes.

> Not published to npm yet. Install it from this repository: `npm install /path/to/backd/clients/js`.

```js
import { createClient, localStorageStorage } from '@backd/client'

const backd = createClient({ url: 'https://localhost:8443', realm: 'blog', storage: localStorageStorage() })

await backd.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })

const posts = backd.db('main').collection('posts')
await posts.create({ title: 'Hello', published: true })
const page = await posts.list({ where: { published: true }, orderBy: '-_meta.created_at', limit: 20 })
```

Server-side code can use an API key (never in browsers) for full access and the admin API:

```js
const admin = createClient({ url, realm: 'blog', apiKey: process.env.BACKD_API_KEY }).admin
const user = await admin.users.find('ada@example.com')
```

## Examples

[`examples/blog`](examples/blog) is a small blog built with Alpine.js: sign up, log in, write, publish and delete posts. [`examples/expenses-without-functions`](examples/expenses-without-functions) shares expenses between users, and shows the limits of access rules. From the repository root:

```sh
make example          # from the repository root; then open https://localhost:8443/example/
```

nginx serves the examples, the API and the docs on one origin, so the examples need no CORS settings. They import `@backd/client` through an import map.

## Documentation

The full guide (options, auth events, token storage, collections, concurrent edits, errors, retries, admin API) is in the backd docs, under Clients → JavaScript client (`docs/content/docs/clients/js.md`).

## Development

```sh
npm install              # also builds the type declarations into types/
npm run typecheck        # tsc --checkJs, strict
npm test                 # unit tests (node:test, mocked fetch)
make js-integration      # from the repository root: tests against backd + MongoDB in Docker
```
