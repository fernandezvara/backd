---
title: "Clients"
description: "Libraries for calling backd from applications."
icon: "package"
weight: 800
toc: true
---

Any HTTP client can call `backd`: the whole API is described in [`api/openapi.yaml`](../api/#openapi-specification). Client libraries wrap it for applications.

| Client | Runtimes | Status |
|---|---|---|
| [JavaScript](js/) (`backd-js`) | Browsers, Node 20+, Deno, Bun, edge runtimes | Auth, sessions, collections and admin API available, with [example apps](../examples/); on npm: `npm install backd-js` |
| [Dart](dart/) (`package:backd`) | Flutter mobile and desktop, standalone Dart | Auth, sessions, collections, files, jobs, orgs, push, consents, realtime and generated realm types — everything but the admin API; on pub.dev: `dart pub add backd` |
| [Go](go/) (`pkg/client`) | Services, jobs, tools and tests; standard library only | Auth, sessions, collections, files, jobs, orgs, push, consents, realtime and generated realm types — everything but the admin API; `go get github.com/fernandezvara/backd/pkg/client` |
| [Python](python/) (`backd`) | Services, scripts and notebooks; sync and async over `httpx` | Auth, sessions, collections, files, jobs, orgs, push, consents, realtime and generated realm types — everything but the admin API; `pip install backd` (from the first PyPI release; the repository until then) |
| Kotlin, Swift and others | Anything [openapi-generator](../api/openapi/#generating-clients) supports | Generated from your realm's [OpenAPI document](../api/openapi/); typed calls and models, without a hand-written client's cache, retries and sign-in flows |

[Generated types](types/) are TypeScript, Dart, Go or Python types for a realm's documents, queries, functions and configuration, made by `backd types` from the config.
