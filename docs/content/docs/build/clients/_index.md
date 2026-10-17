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
| Dart, Kotlin, Swift and others | Anything [openapi-generator](../api/openapi/#generating-clients) supports | Generated from your realm's [OpenAPI document](../api/openapi/); typed calls and models, without the JavaScript client's cache, retries and sign-in flows |

[Generated types](types/) are TypeScript types for a realm's documents, queries, functions and configuration, made by `backd types` from the config.
