---
title: "Clients"
description: "Libraries for calling backd from applications."
icon: "integration_instructions"
weight: 600
toc: true
---

Any HTTP client can call `backd`: the whole API is described in [`api/openapi.yaml`](../api/#openapi-specification). Client libraries wrap it for applications.

| Client | Runtimes | Status |
|---|---|---|
| [JavaScript](js/) (`backd-js`) | Browsers, Node 20+, Deno, Bun, edge runtimes | Auth, sessions, collections and admin API available, with [example apps](../examples/); on npm: `npm install backd-js` |

[Generated types](types/) are TypeScript types for a realm's documents, queries, functions and configuration, made by `backd types` from the config.
