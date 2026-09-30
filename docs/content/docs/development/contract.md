---
title: "API contract"
description: "api/openapi.yaml, the contract tests that keep the server true to it, and the lint."
icon: "handshake"
weight: 920
toc: true
---

`api/openapi.yaml` is the contract of the HTTP API, in OpenAPI 3.1. Clients, such as the [JavaScript client](../../clients/js/), are written against it, so the server must never drift from it.

## What it covers

- Every route: health probes, `/_auth`, `/_admin` and the generic document routes.
- Every status an operation documents explicitly, plus a `default` for errors that can happen anywhere (`404` for an unknown realm, `413`, `415`, `500`, `503`).
- Response bodies, including the error envelope and its closed list of `code` values.
- Response headers clients rely on: `ETag`, `Location`, `Retry-After`, `WWW-Authenticate`, `X-Request-ID`, and `Cache-Control: no-store` on account data.
- Documents as open objects. Their fields depend on each collection's `schema.json`, but `id` and `_meta` are described exactly.

## How it's enforced

Two checks keep the spec honest, and both run in CI.

**Contract tests** (`internal/httpapi/contract_test.go`) run the real HTTP handler and check every response against the spec:

- the operation is found from the router's own route pattern, and the status must be listed on the operation; `default` only stands for `404`, `413`, `415`, `500` and `503`;
- JSON bodies are validated against the spec's schemas (OpenAPI 3.1 schemas are JSON Schema 2020-12), with formats such as `date-time` asserted;
- required headers must be present and match their schemas; operations without a body must not send one.

They also check coverage in both directions: every route of the server is in the spec, every operation in the spec is a route, and **every explicitly documented status is produced by at least one test**. A separate test feeds deliberately broken responses to the checker to prove it catches them.

**Lint**: `make lint-api` runs Redocly's strict ruleset, so the spec is valid OpenAPI and warnings fail. Deliberate exceptions are listed in `api/.redocly.lint-ignore.yaml` with the reason.

## Changing the API

Change the spec in the same commit as the server. If a handler starts returning a new status, a new field or a new header, the contract tests fail until the spec says so, and a newly documented status fails until a test produces it.
