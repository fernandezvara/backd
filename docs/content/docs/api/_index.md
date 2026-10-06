---
title: "HTTP API"
description: "Request IDs, request bodies, errors and health endpoints."
icon: "api"
weight: 300
toc: true
---

## OpenAPI specification

The whole HTTP API is described in [`api/openapi.yaml`](https://github.com/fernandezvara/backd/blob/main/api/openapi.yaml) (OpenAPI 3.1): every endpoint, status, response body and header. The server is tested against it on every change (see [API contract](../development/contract/)), so it can be used to generate or check clients. Collections' fields depend on your configuration, so documents appear there as open objects with exactly described `id` and `_meta`.

## Request IDs

Every request gets a request ID:

- If the client sends a valid `X-Request-ID` header, `backd` uses it: 1–128 characters from `A–Z a–z 0–9 . _ : -`.
- Otherwise `backd` generates one.
- The ID is always returned in the `X-Request-ID` response header, included in every log line for the request, and included in error bodies.

## Request bodies

- Bodies larger than `MAX_BODY_BYTES` (default 1 MiB) are rejected with `413 payload_too_large`.
- `POST` and `PUT` require `Content-Type: application/json`.
- `PATCH` requires `Content-Type: application/merge-patch+json`.
- The only parameter allowed on these content types is `charset=utf-8`. Anything else returns `415 unsupported_media_type`.

## Caching

Responses depend on who is asking, so `backd` tells caches (browsers, proxies, CDNs) how to keep them apart:

| Responses | Headers |
|---|---|
| Everything under `/v1/` | `Vary: Origin`, whether or not the request has an `Origin` header, so an answer for one web origin is never served to another |
| Document routes (`/v1/{realm}/{database}/{collection}…`) | Also `Vary: Authorization`, so a response cached for an anonymous caller is never served to a signed-in one, or the other way around |
| Document routes, when the request has an `Authorization` header | Also `Cache-Control: private, no-cache`: shared caches never store the response, and browsers revalidate it before reusing it |
| `/_auth`, `/_admin`, and [function](../functions/) calls (`_func`, `_jobs`) | `Cache-Control: no-store`: tokens, account data and function results are never cached |

Anonymous document responses carry no `Cache-Control`, so a proxy or CDN may cache public data, for example published posts, by its own rules. The `Vary` headers make that safe. Errors on these routes carry the same headers.

{{< hint style="tip" title="Already done for you" >}}
You don't configure any of this: `backd` sets `Vary` and `Cache-Control` on every response, so a shared cache can't serve one caller's data to another. Only cache public, anonymous responses on purpose, and never strip these headers at your proxy.
{{< /hint >}}

## Errors

Every error uses the same JSON envelope. Success responses are never wrapped.

```json
{
  "error": {
    "code": "validation_error",
    "message": "document failed schema validation",
    "details": [
      { "path": "email", "reason": "must match format \"email\"" }
    ],
    "request_id": "d2fk1a30h2ipb32i2hh0"
  }
}
```

`details` is omitted when empty. `code` is stable and meant for programs to check; `message` is meant for humans.

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_json` | Body is not a single valid JSON object |
| 400 | `validation_error` | Document fails its schema, or an `/_auth` or `/_admin` request body is invalid (`details` per path) |
| 400 | `invalid_header` | Malformed `If-Match` header, or an [`X-Backd-On-Behalf-Of`](../auth/api-keys/#acting-on-behalf-of-a-user) header that isn't allowed or names no user of the realm |
| 400 | `invalid_query` | Bad `where`, `order_by`, `limit`, `skip` or `count`, or unknown query parameter |
| 400 | `idempotency_key_required` | A [function](../functions/calling/#idempotency) with `idempotency: required` was called with no `Idempotency-Key` |
| 401 | `unauthenticated` | Missing, unknown, expired or revoked session token or API key, where credentials are required (see [API keys](../auth/api-keys/#who-can-access-data)) |
| 401 | `invalid_credentials` | Wrong email or password |
| 403 | `forbidden` | The operation isn't allowed: sign-up in a realm where it isn't open, no [access rule](../auth/rules/) allows it, a session on the [admin API](../auth/admin/), or acting on behalf of a disabled user |
| 404 | `not_found` | Unknown route, realm, database, collection or `id`, or a document the caller's access rules hide |
| 405 | `method_not_allowed` | Method not supported on the route |
| 409 | `conflict` | A unique index is violated (`details` name the fields) |
| 409 | `write_conflict` | A PUT, PATCH or conditional DELETE lost the race against concurrent writes 3 times |
| 409 | `email_taken` | Sign-up with an email that is already registered |
| 409 | `request_in_progress` | A [function](../functions/calling/#idempotency) call with this `Idempotency-Key` is still running |
| 412 | `version_mismatch` | `If-Match` doesn't match the current version |
| 403 | `invalid_file_link` | A [file link](../files/transfers/#links-for-apps) that was changed, has expired or whose key was rotated |
| 404 | `file_missing` | The document holds a [file](../files/transfers/#downloading) the storage no longer has |
| 409 | `too_many_files` | A `multiple` [file field](../files/file-fields/) is at `max_files` |
| 409 | `upload_mode_mismatch` | A proxy upload to a [direct](../files/transfers/#direct-uploads) field, or a direct start on a proxy field |
| 409 | `file_not_uploaded` | A direct upload was completed before its file reached the bucket |
| 413 | `payload_too_large` | Body exceeds `MAX_BODY_BYTES`; an upload, `max_size` or `BACKD_MAX_UPLOAD_BYTES` |
| 415 | `unsupported_file_type` | The type detected from an upload isn't one the field accepts |
| 416 | `range_not_satisfiable` | A `Range` outside the file |
| 415 | `unsupported_media_type` | Wrong `Content-Type` for the method (a `webhook` function's body isn't required to be JSON at all) |
| 422 | `upload_mismatch` | A direct upload's size or SHA-256 isn't the declared one: the object was deleted |
| 422 | `idempotency_key_reused` | A [function](../functions/calling/#idempotency) `Idempotency-Key` was already used with different input |
| 429 | `too_many_requests` | Too many failed logins (see [brute-force protection](../auth/sessions/#brute-force-protection)); or a [function](../functions/calling/#concurrency-limits), or its realm, is at its concurrency limit; or the caller is over a function's [rate limit](../functions/calling/#rate-limits). Wait `Retry-After` seconds |
| 500 | `function_failed` | A [function](../functions/calling/#calling-a-function) threw an error other than `ctx.error`, crashed, or went over its memory or CPU limit |
| 500 | `internal_error` | Unexpected failure. Details are only in the logs |
| 500 | `invalid_output` | A [function](../functions/calling/#calling-a-function)'s output is larger than `max_output`, or doesn't match `output.schema.json` |
| 500 | `secret_missing` | A [function](../functions/secrets/) declares a secret that isn't set for its database or realm |
| 503 | `storage_unavailable` | A realm's [file storage](../files/storage/) can't be reached, or its keys aren't set |
| 503 | `unavailable` | Not ready, MongoDB unreachable, `MONGO_OP_TIMEOUT` exceeded, too many password operations at once, or the [functions](../functions/running/#running-the-executor) executor isn't configured, doesn't answer, or is at capacity (all with `Retry-After` where the caller can usefully retry) |
| 504 | `function_timeout` | A [function](../functions/calling/#calling-a-function) didn't finish within its `timeout`; it was stopped |

A function's own errors, thrown with `ctx.error(status, code, message)`, carry whatever 4xx status and lower-case code it chose — see [Calling a function](../functions/calling/#calling-a-function).

## Health endpoints

These live outside the `/v1` prefix:

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | The process is alive: always `200 {"status":"ok"}` |
| `GET /readyz` | Config loaded, provisioning done and MongoDB reachable: `200 {"status":"ready","config":"<fingerprint>"}`, otherwise `503 unavailable`. `config` is the fingerprint of the config the instance runs (see [Deploying config](../operations/deploying/)) |
