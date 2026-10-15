# Design: triggers and realtime

- **Issues:** features 27.1–27.22 (milestone `27-events`), `support_docs/issues/roadmap-issues-events.md`
- **Status:** decided; merges and supersedes the earlier separate realtime and triggers designs (retired) and backlog item 7.21
- **Related designs:** [internal-functions.md](./internal-functions.md) (jobs, `ctx.admin.db`, origins, `retry:`), [team-access.md](./team-access.md) §7 (the organization hooks), [account-lifecycle.md](./account-lifecycle.md) (`account.on_signup`, the deferred `account.on_delete`), [email-delivery.md](./email-delivery.md) (the named delivery function, a different contract), [organizations.md](./organizations.md) (memberships, immediate revocation), [mfa-passwordless.md](./mfa-passwordless.md) (sessions, MFA flag), [reference-expansion.md](./reference-expansion.md) (client reference cache)

## 1. The idea

Two features, one library: **triggers** run a function when a collection's data changes and a rule matches; **realtime** pushes changes to connected clients under their own access rules. Both read MongoDB change streams, so decoding, pre-change images, the namespace filter, the system projection and resume handling are built once, in `internal/changefeed`, and used by two consumers with different needs (§3).

- **Change feeds, not the request path.** A consumer reads MongoDB changes, evaluates `when:`/`where` in memory, and either enqueues an ordinary async job (triggers) or sends an SSE event under the subscriber's rules (realtime). Writes never wait on the executor, and nothing is lost when a worker is down.
- **One vocabulary.** A change is `created`, `updated` or `removed` everywhere: triggers' `events:` lists them, realtime events are named them. For a trigger, `removed` means the document was deleted; for a subscriber it also covers a document that stopped being visible (§18) — same word, documented once.
- **MongoDB is the bus.** No broker, no messaging between instances.
- **Honest costs.** Pre-change images, a function run per event and per-subscriber matching are real costs (§6). They are opt-in per collection, bounded, metered and measured before the rest of the milestone builds on them.
- **No silent loss.** A trigger event is either delivered, or recorded as a gap an operator can see and rescan (§9.3, §13).

**Not included:** before-write or synchronous triggers (rejected — every write would wait on the executor); ephemeral messages (presence, typing, broadcast — channels are a later design); user-declarable triggers on system collections (§14); the `email.data_hook`/`pages.data_hook` template hooks (§15); encrypted fields and DocumentDB, which are out of scope for this milestone (§28).

## 2. Decisions at a glance

**Shared machinery**

| Topic | Decision |
|---|---|
| Structure | `internal/changefeed` is a library; two consumers each own one deployment-wide `watch()`: realtime (in-memory position, lazy) and the trigger dispatcher (durable position, lease holder only) |
| Watched set | every `realtime: true` and `triggers:` collection from the loaded configuration, plus a projected set of system collections; re-opened only on config reload |
| Images | `fullDocument` and `fullDocumentBeforeChange` as `whenAvailable` from pre/post-images; never `updateLookup` |
| Pre-images | enabled by `backd provision` (or auto mode); `verify` fails startup naming collection and command; **never** disabled automatically |
| Requirements | MongoDB 6.0+ replica set; a user with change-stream privilege on the deployment; documented role, clear startup error |
| System watch | sessions, users, roles, memberships, teams, organizations, consents, API keys — projected to the minimum fields server-side, noise filtered |
| Flags | `BACKD_EVENTS` (consumers on this instance), `BACKD_TRIGGERS` (may hold the dispatcher lease) |
| Cost control | consumers open lazily; measurement gate in 27.1; buffer and memory budgets; defaults set from measurements |

**Triggers**

| Topic | Decision |
|---|---|
| Declaration | `triggers:` in `collection.yaml`: `events`, optional `when`, `function`, optional `fields`, `session`, `ordered` |
| `when:` | `CompileTrigger`: `document`, `before`, `changed()`, `now`, `config`; no `user`/`data`; `now` is the change's cluster time |
| Dispatch | one lease and one watermark per deployment; the holder runs its own stream resumed at the watermark; handoff is just the new holder starting there |
| Watermark | advances on every observed change (and on idle post-batch tokens), batched, written only after jobs are queued, fenced by the lease epoch |
| Event id / job key | `event.id` = hash(namespace, document key, cluster time, operation, version); job key = hash(`event.id`, trigger), unique index on jobs |
| Guarantee | at-least-once; replays collapse on the job key; handlers stay idempotent |
| Payload | ids, `version`, `changed` by default; bodies only through `fields:` |
| Function | `internal: true`, `mode: async`; `ctx.user` is `null`, `ctx.admin.db` always present; startup-checked and visible in listings |
| Chains | `_meta.chain = {root, depth}` stamped at the write boundary of a trigger run and propagated through `ctx.call`; `max_chain` 16 |
| Overload | first-party events are never rate-dropped; the burst cap applies to chained events only; a per-realm backlog ceiling records a gap instead of stalling others |
| Gaps | window loss, backlog ceiling and missing pre-images are recorded; `backd triggers status` lists them, `backd triggers rescan` re-emits from `_meta.updated_at` |
| System events | named aliases only; legacy hook paths stay until per-event parity tests pass |

**Realtime**

| Topic | Decision |
|---|---|
| Subscriptions | a collection with a `where` filter (no `sort`/`limit`) or one document; events `created`/`updated`/`removed` |
| `removed` | for deletes and for documents that stop matching; only to subscribers who could see the document before |
| Transport | `POST /_realtime` returning SSE, read with `fetch`; multiplexed subscriptions; heartbeat |
| Resume token | MongoDB token + realm + principal + issue time, signed with a per-realm key from the system database; not bound to the subscription set |
| Rules | `where`, `read` in memory per subscriber, pinned to the `storage.Match` subset; access loaded at open and refreshed on changes |
| Access changes | `session_ended` on revocation; `reset` (`access_changed`) only when a subscriber's effective access actually changed; `reauth` at expiry — sessions and API keys alike |
| Limits | `realtime: true` opt-in; per stream, user and instance; 256 KiB buffers and an instance memory budget; anonymous by read rule |
| Expansion | none in events; the client fills references from its cache with batched reads |
| Missing pre-image | the collection's subscriptions get `reset` (`preimage_missing`) — never `removed`, which would leak ids |

# Part I — Shared machinery (issues 27.1, 27.2)

## 3. The change feed library and its two consumers (issue 27.1)

**Delivery is the Change Streams API** — `watch()`, never `local.oplog.rs` tailing. Change streams are the supported, portable mechanism: they work on replica sets and Atlas, expose pre-change images, and hand back resume tokens that survive failovers.

**The library** (`internal/changefeed`) owns what both features need and nothing feature-specific:

- the pipeline builder: `$match` on `ns.db`/`ns.coll` for a watched set, and the system projection of §4;
- decoding a change into `{namespace, realm, operation, document key, cluster time, before, after, version}`;
- operation mapping: `insert` → `created`; `update` **and `replace`** → `updated` (backd writes with replace — `repository.go` uses `ReplaceOne` — so there is often no update description); `delete` → `removed`;
- `changed`: the path diff of pre- and post-image, the same function write rules use for `changed()`, never the update description;
- image options: `fullDocument: whenAvailable` and `fullDocumentBeforeChange: whenAvailable`. `updateLookup` is never used — it returns the document as it is *now*, which can be a later state than the event and would misclassify `created`/`removed`;
- resume handling and error classification (`ChangeStreamHistoryLost` → gap, §9.3);
- read preference `secondaryPreferred` where the topology allows — events carry their images, so no extra lookups hit the primary.

**Two consumers, two streams.** Sharing one physical stream would make every instance decode every trigger collection "because any instance may become dispatcher", and a first realtime subscriber would re-open the stream the dispatcher depends on. So each consumer opens its own `watch()` on the deployment:

| | Realtime consumer | Trigger dispatcher |
|---|---|---|
| Runs on | any instance with `BACKD_EVENTS=true` that has subscribers | the lease holder only (`BACKD_TRIGGERS=true`) |
| Starts | lazily, on the instance's first subscriber, from "now" | on lease acquisition, at the stored watermark |
| Position | in memory; clients resume with their own tokens | durable (§9) |
| Namespaces | `realtime: true` collections + system collections | trigger collections + the system collections built-in events need |
| Idle | closes after a grace period with no subscribers | n/a |

An idle API replica therefore reads nothing. `$match` reduces what is decoded and sent, not what the server scans; each open consumer pays for the deployment's total write rate (§6).

**Instance flags**

| Setting | Default |
|---|---|
| `BACKD_EVENTS` | `true` — this instance may run the realtime consumer and serve `/_realtime` |
| `BACKD_TRIGGERS` | `true` — this instance may hold the dispatcher lease |

Dedicated instances: API replicas with `BACKD_EVENTS=false`, realtime instances with `BACKD_EVENTS=true BACKD_TRIGGERS=false`, dispatcher candidates with `BACKD_TRIGGERS=true`; the proxy routes `/_realtime` accordingly.

**The measurement gate.** 27.1 ends with a benchmark before anything builds on it: write latency and storage on a representative collection with and without pre-images; oplog-scan CPU with the cluster under write load; one consumer versus several. The results are written into the issue, and the defaults of §6 are set from them.

## 4. What is watched, and what is never read (issue 27.1)

**The watched set is static per configuration.** Every `realtime: true` and `triggers:` collection of every loaded realm, plus the system collections below. A configuration reload that changes the set re-opens the affected consumers with `resumeAfter` the last token; subscribers arriving or leaving never change the set. (Idle collections cost one in-process drop per event.)

**Encrypted fields are out of scope.** Field encryption is a future milestone with its own design; nothing here decrypts, reveals or special-cases such fields. Whichever of the two milestones lands second owns the integration (§28).

**System collections are projected server-side.** Immediate revocation needs to hear about sessions, users, API keys, organizations, memberships and teams (consents live in the user's document), but not about their values. The consumer's pipeline reduces each event of a system collection, where it is read, to the document key, the operation and **the names of the fields that changed** — never a value. That is enough: a session is closed by its id, a user by `users` `_id`, a membership by its `org/user` id, a team or organization by its id; the consumer then reloads the access of the streams that hold them from the database, under the normal rules.

| Collection | What tells the stream to act |
|---|---|
| sessions | creation, deletion (a revoked or ended session is deleted); any update beyond last use and sliding expiry |
| users | `roles`, `disabled`, `erased_at`, `consents.*` among the changed field names; deletion |
| API keys | deletion, `scopes`, `role`, `expires_at`; not last use |
| organizations | `require_mfa`, `claims.*`, `status`, `deleted_at` among the changed field names |
| memberships | any creation, change or deletion (the key is `org/user`) |
| teams | `roles`, `name` among the changed field names; deletion |

The dispatcher's built-in events (27.9) read their own, equally minimal projection of the same collections.

- **Password hashes, TOTP secrets, recovery codes, token hashes and provider data never leave the database.**
- **Noise is filtered in the pipeline:** an update whose only changed fields are a session's last-use time and sliding expiry (written at most once a minute per session), or an API key's last use, is dropped by `$match` on the changed field names. At 100,000 active sessions that is about 1,600 events per second of nothing.
- **Events are never logged**, and an unexpected decode failure logs the namespace and operation only.

## 5. Pre-images, provisioning and requirements (issue 27.2)

`changeStreamPreAndPostImages` is a per-collection MongoDB setting that makes both features correct (`before`, `removed`). It has a storage and write cost (§6), and disabling it is irreversible — the stored pre-images are deleted. So it follows the provisioning model, not startup magic:

- **Enabling** is part of `backd provision` (and of `PROVISION_MODE=apply`, the default): idempotent, for every `realtime: true` and `triggers:` collection. It needs the `collMod` privilege, like index creation.
- **`PROVISION_MODE=verify`** (the production mode): startup **fails** if a collection needs pre-images and lacks them, naming the collection and the command to run.
- **Disabling is never automatic.** Removing `realtime:`/`triggers:` leaves the setting in place (a rolling deploy with an older configuration must not lose `before`). `backd provision --disable-preimages` does it explicitly, documented as irreversible. Startup **warns** — never fails — about collections with pre-images that nothing uses, naming the storage cost.

**Requirements**

- **MongoDB 6.0+ replica set** (`fullDocumentBeforeChange`). Startup refuses a realm needing pre-images on anything older, naming the requirement and the collections. There is no degraded mode.
- **Privilege.** Opening a deployment-wide `watch()` needs the `changeStream` and `find` actions on every database (for example the built-in `readAnyDatabase` role, or a custom role with those actions) — MongoDB checks the scope of the `watch()` call, not the `$match`. Startup probes it and, when missing, fails with a message naming the role and why. A database-scoped mode for shared clusters with restricted users is a follow-up (§28); nothing here blocks it.
- **The resume window** is the MongoDB oplog size. A consumer or dispatcher down longer loses its position (§9.3); the docs explain sizing it from write volume and acceptable downtime.

## 6. Cost model and defaults (issues 27.1, 27.19, 27.20)

Honest accounting, so nobody discovers it in production.

**Inherent costs**

| Cost | Who pays | Notes |
|---|---|---|
| **Pre-image write amplification** — every update/delete on an opted-in collection stores the previous document too (about double the bytes for a replace); storage grows with updates × document size over the window | MongoDB primary, disk | needed for a correct `removed` and `before`; per-collection opt-in limits the blast radius |
| **A function run per matching event** — one Deno process per call, plus one job insert and one claim | executor and workers | the real cost of triggers, far above the stream; `fields:`, narrow `when:` and `max_chain` matter; a bulk import queues one run per matching document |
| **Unkeyed subscriptions are O(subscribers) per change** | API CPU | the cost guide steers filters to an equality on a grouping field |
| **Each open consumer scans the deployment's whole oplog** — `$match` reduces decoding, not scanning | MongoDB | a cluster hosting unrelated heavy writes pays for them in every open consumer; hence lazy consumers |

**Measured by the gate (27.1)** — one developer machine, a single-node MongoDB 8, so indicative: the ratios matter. Reproduce with `BACKD_MEASURE=1 go test ./internal/mongodb -run MeasurementGate -v`.

| What | Result |
|---|---|
| Replace latency, pre-images on vs off | within run-to-run noise up to 16 KiB documents; at 256 KiB with 8 concurrent writers p99 rose 3–4× and throughput fell ~40% |
| Storage | one extra copy of the document per update/delete (3,000 replaces of 16 KiB: +47 MiB), kept for as long as the change history is |
| One consumer draining a backlog | 40–75k changes/s with no other writes; ~9.5k watched changes/s with 100 unrelated writes per watched one (the scan is proportional to the deployment's total writes) |
| 1, 2, 4, 8 consumers on the same backlog | 77k, 128k, 213k, 272k changes/s in total; each took 65 → 147 ms to drain 5,000 — roughly 2× slower each at 8 |

What this changes: nothing in the decisions. Pre-images stay opt-in per collection, with the documentation steering large, frequently updated documents away from them; consumers stay lazy, since idle replicas would each add a scan.

**Required mitigations** (acceptance criteria of the issues named):

1. **Lazy consumers (27.1):** the realtime consumer opens on the first subscriber and closes when idle; the dispatcher runs only on the lease holder. N API replicas never mean N scans.
2. **Session noise (27.1):** last-use-only updates are filtered server-side (§4).
3. **Bounded memory (27.13):** `BACKD_REALTIME_BUFFER` defaults to 256 KiB per stream, and `BACKD_REALTIME_MEMORY` is an instance-wide budget; over budget, the largest buffers get `slow_consumer` resets. 10,000 streams × 1 MiB would otherwise be 10 GiB in the worst case.
4. **No reset storms (27.15):** the organization's access is computed once per change, a subscriber is reset only when their effective access differs before and after, and the client reloads with jitter.
5. **Batched enqueue (27.4):** matches of a batch of changes go in one unordered insert (duplicate keys tolerated) and the watermark moves once per batch, not per event.

**Defaults** in this design are provisional ("set from measurements") until 27.1's gate and the load tests 27.19/27.20 write results into their issues.

**Known and documented, not fixed here:** the function input is one event per run. A `batch:` option — an array of events per run, opt-in per trigger so nothing existing breaks — is a follow-up (§28); until then hot collections want `fields:` and narrow `when:`.

# Part II — Triggers

A collection can declare that when its data changes and a rule matches, backd runs a function. State machines, pipelines, fan-out, denormalization — declared as reactive rules of the collection, not as calls the app must remember to make.

- **A trigger is a collection rule that is reactive.** `triggers:` lives in `collection.yaml`, next to `rules:` and `on_owner_delete`.
- **Pipelines are first-class.** Each state transition is its own trigger pointing at its own function, all against the same document's `status`. Chains are legitimate and bounded, so loops can't run away.
- **One mechanism for "backd calls a function when something happens"** — `account.on_signup` and `organizations.hooks.*` move onto the dispatcher as named aliases (§14), event by event, once parity is proven.

## 7. Configuration (issue 27.3)

```yaml
# collection.yaml
triggers:
  on_paid:
    events: [created, updated]         # created | updated | removed; at least one
    when: "document.status == 'paid' && before.status != 'paid'"
    function: billing/on-paid          # <database>/<function>, internal and async
    fields: [status, total]            # optional: bodies in the job, trimmed to these paths
    session: true                      # optional: the input gains `by`
    ordered: true                      # optional: runs for one document are serialized (§12)
  on_merged:
    events: [updated]
    when: "document.status == 'merged' && 'status' in changed()"
    function: billing/post-merge
```

- **Trigger names** follow function-name rules; a collection may declare at most 20 (`triggers.max_per_collection`, realm-lowerable).
- **`function`** must exist in the realm and be `internal: true` and `mode: async`, checked at startup through the same `hookFunction` validator the existing hooks use; the error names the collection file and trigger.
- **Payload is minimal by default.** Without `fields:`, the input carries ids, `version` and `changed`, and **no document bodies**: the function reads what it needs through `ctx.admin.db`. `fields: [a, b.c]` adds `before`/`after` trimmed to those paths (plus `_meta`); `fields: ["*"]` adds whole documents. Jobs are stored for `functions.log_retention`, so bodies in inputs outlive erasure of the data they copy — bodies are an explicit choice. A job input over the job-input cap is delivered with `truncated: true` and ids only.
- **Nothing in `function.yaml` marks a trigger function.** A function can't "be a trigger": the reactive part is the collection's declaration; the function is an ordinary internal async function an administrator can also run by hand (§10).
- **Reload semantics:** a trigger added, removed or reconfigured applies to the **next change onward** — no replay. Removing `triggers:` stops matching at once; in-flight jobs still run. A `when:` that doesn't compile refuses the reload.

```yaml
# realm.yaml
triggers:
  max_chain: 16                  # writes caused by a trigger may chain at most this deep
  max_chained_per_minute: 60     # per trigger: chained events above this in a minute are dropped (§11)
  max_per_collection: 20
  max_backlog: 100000            # queued trigger jobs per realm before a gap is recorded (§11)
```

## 8. Events and `when:` (issue 27.3)

The dispatcher decodes each change on a trigger collection (§3 maps the operation) and builds:

- `event` — `created`, `updated` or `removed`;
- `document` — the post-change document; `null` on `removed`;
- `before` — the pre-change image; `null` on `created`;
- `changed` — the path diff of before and after (§3), empty on `created`/`removed`.

`when:` compiles against the collection's schema with a trigger scope (`CompileTrigger`, next to `CompileInvoke`/`CompileConfig`): `document`, `before`, `changed()`, `now`, `config`. **No `user`, no `data`** — an event has no caller. Field paths are checked like write rules, so a typo is a startup error, not a silently dead trigger. **`now` is the change's cluster time**, not the wall clock, so a replay evaluates `when` exactly as the first delivery did.

Semantics: `when` absent → every matching event fires. A `when` that references `document` never matches a `removed` event; rules that should fire on removals check `before`. The pipeline pattern is `before.x != v && document.x == v` — a transition, so the same value written twice doesn't re-fire — and `'status' in changed()` narrows further when several triggers share a collection. `when` is a gate for enqueueing, not a permission.

## 9. Dispatch (issue 27.4)

One dispatcher serves the whole deployment, because the stream it reads is deployment-wide: per-realm leases would only add coordination. Per-realm behavior is metered, not coordinated.

1. **Lease.** A single document in the deployment database (`backd___deployment`, which already records provisioning fingerprints): `{holder, epoch, expires_at}`. The lease, watermark and gaps are runtime state written by serving instances in every `PROVISION_MODE`; only the schema around them (the unique job-key index, pre-images) is provisioned. Acquired and renewed with MongoDB server time (never instance clocks), renewed every third of the TTL (default 15s); every acquisition increments `epoch`. Only `BACKD_TRIGGERS=true` instances compete.
2. **Stream.** The holder opens its own deployment-wide `watch()` on the trigger namespaces (and the system collections built-in events need), `resumeAfter` the stored watermark.
3. **Batch loop.** Read a batch of changes → evaluate every trigger's `events`/`when` once per change → insert all resulting jobs in one unordered insert (duplicate keys tolerated, §9.1) → write the watermark.
4. **Watermark.** `{epoch, token, at}`, one per deployment. It advances on **every** observed change and on idle post-batch tokens, not only on matches — a quiet deployment's position keeps moving, so a handoff never replays a whole oplog or falsely hits the window. It is written only after the batch's jobs are durably queued, as a conditional update on `epoch`.
5. **Fencing.** A holder that lost the lease (a pause, a partition) fails its next watermark write on the epoch and stops. Jobs it may still insert collapse on the job key, so fencing needs no coordination beyond the watermark.
6. **Handoff.** The new holder acquires the lease (epoch + 1), reads the watermark and opens its stream there. Events between the watermark and "now" are re-evaluated and collapse on their job keys. There is no second catch-up reader: the normal stream *is* the catch-up.
7. **Enqueue.** Each match is an ordinary async job — `ActsAsFunction`, `CallerActor` system, `Origin: trigger:<db>/<collection>/<trigger>` — with the input of §10.
8. **Reload.** A change to the set of trigger collections re-opens the stream at the last processed token.

### 9.1 Event id and job key

- **`event.id`** = hash(namespace, document key, cluster time, operation, `_meta.version`). It does not use the resume token (token bytes aren't a stable identity) and does not rely on `(id, version)` alone (delete and recreate with a custom id repeats version 1). Identical for the same change seen twice; meaningless to anyone without the stream.
- **Job key** = hash(`event.id`, trigger name). One change can match several triggers, so the event id alone would collapse their jobs. The jobs collection gets a **unique index on the job key**, created by the provisioner like every index (and reported missing by `verify`); an insert that hits it is a dedup hit.
- Functions that must be exactly-once in effect deduplicate on `event.id` (the docs show a seen-ids collection pattern); `functions history` shows it.

At-least-once remains possible — a job can fail after enqueue and `retry:` re-runs it — so handlers are written idempotent regardless.

### 9.2 Ordering

No order is promised between events, or between an event and the write that caused it. The input carries `version` and `changed`; a handler that must not apply stale work compares them against the current document — the same reconciliation realtime gives clients. A trigger that needs serialization declares `ordered: true` (§12).

### 9.3 Degraded delivery: gaps

Three ways delivery is imperfect. All fail **closed and loud**, and each leaves a **gap record** in the system database (`{realm, collection, trigger?, from, to, reason, count}`; per-document entries capped at 1,000 per gap) that `backd triggers status` lists and `backd triggers rescan` can act on (§13):

- **Window missed.** The dispatcher was down (or lagged) longer than the change-stream retention: resuming fails with history lost. It restarts from "now", records the interval from the stale watermark to now, and meters `trigger_window_missed`. Events inside the interval are gone from the stream; rescan is how an operator recovers them.
- **Missing image.** An update/delete arrived without `fullDocumentBeforeChange` (retention shrunk below the window, or expired early). A `when` that references `before` or `changed()` can't be evaluated: the trigger is skipped for that event, metered `trigger_missing_preimage`, logged, and recorded with the document id.
- **Backlog ceiling.** See §11.

## 10. The function contract (issue 27.5)

```js
export default async function (input, ctx) {
  // input: { id, event, collection, doc: {id, version}, version, changed,
  //          before?, after?, by?, depth, root, at, truncated? }
}
```

- **`internal: true`, `mode: async`.** Triggers name only internal async functions; startup enforces it through the shared validator.
- **Identity.** `ctx.user` is `null` and `ctx.db` acts as anonymous — the event has no caller to impersonate. `ctx.admin.db` is **always present**, like scheduled runs: the realm declared the trigger, so its function is trusted with the realm's data; `admin: true` in `function.yaml` is not required for trigger runs (it still governs HTTP/`ctx.call` use of the same function). `secrets:` and `network:` remain the function's own declarations. **This is visible**: `backd triggers status` and its admin route list every trigger with its function and the note "runs with admin data access", so the grant is reviewable in one place (there is no functions listing command to hang it on).
- **`session: true` — who wrote.** The input gains `by`: the user id of the writer when `_meta.updated_by` (or `created_by` on creates) is a `user:` subject; absent when the write came from an API key, another function or backd itself, **and absent when the value is the erase sentinel** (`backd:erase`). `by` is **informational** — never an authenticated identity: the function still runs as admin and a `by` grants nobody anything. On removals, `by` is the last *writer* from the pre-change image, not the deleter; the docs say so.
- **Failures.** One shot, ok or ko — unless the function declares `retry:` (the existing mechanism, unchanged). A `FunctionError` is permanent; anything else retries. A failed *enqueue* is not a function failure: the watermark doesn't advance and the batch replays.
- **Records.** Trigger runs are ordinary invocations: `origin: trigger:<db>/<collection>/<trigger>`, `actor` system, plus `event_id`, listed by `backd functions history` and expiring after `functions.log_retention` (default 7d).
- **Writes inside a run** are stamped `_meta.chain = {root, depth}` (§11). `_meta.chain` is reserved: a client can't set it, and it appears in `_meta` of the written document (OpenAPI and typegen updated).
- **Manual runs.** `POST /_admin/functions/{db}/{name}/invoke` (and `client.admin.invokeFunction`) run a trigger's function by hand with an input the caller supplies — it neither "fires the trigger" nor requires an event. The reactive wiring is in `collection.yaml`, not in the function.

## 11. Chains, loops and backpressure (issue 27.6)

A trigger function's own writes can fire triggers — that is the pipeline feature, and also how a bug becomes an infinite loop.

1. **Lineage is stamped, not looked up.** Every trigger input carries `root` (the originating event's id) and `depth` (0 for a first-party change). Every write made inside a trigger run — directly or through `ctx.call` of other functions — gains `_meta.chain = {root, depth}` at the write boundary, from the invocation's context: the lineage is bound to the invocation's short-lived internal credential, which the data API behind `ctx.db`/`ctx.admin.db` already receives per call, so a function can't forge or drop it. The dispatcher reads it straight from the change: no lookup, no TTL record, correct across handoffs. The new event's `root` is that root and its `depth` is one more.
2. **The cap.** `triggers.max_chain` (default 16): a chained event deeper than the cap is not enqueued; logged and metered (`trigger_dropped{reason=chain}`), and the document keeps its `_meta.chain` so the break is visible in the data.
3. **The burst backstop applies to chained events only.** `max_chained_per_minute` (default 60) per trigger bounds any hot loop shallower than the cap. Excess *chained* events are dropped, logged and metered (`burst`).
4. **First-party events are never rate-dropped.** An import of 10,000 documents fires 10,000 `created` events; none is discarded for being many. The overload response is backpressure, in this order: the backlog grows in the jobs collection (durable, metered as `trigger_backlog`), workers drain it at their concurrency, and the soft limit alerts. Loops can't pass through this path, because a loop is by definition chained.
5. **Backlog ceiling.** `max_backlog` (default 100,000 queued trigger jobs per realm; provisional) is the one place first-party events stop being enqueued: past it, that realm's trigger events are **recorded as a gap** (`reason: backlog`) instead of enqueued — storage stays bounded and **other realms are not stalled**, which is why the ceiling is per realm and not a pause of the shared stream. The gap is visible and rescannable (§13).
6. **Self-guard by construction.** Transition-style `when` makes the common loop impossible to express; the docs recommend it and show the commit → PR → merged pipeline with it.

## 12. Ordered triggers (issue 27.7)

Pipelines on one document (`status` changing three times quickly) can run their steps concurrently and out of order. `ordered: true` on a trigger gives its jobs a serial key (`<trigger>|<document id>`): the worker's claim skips a job while another with the same key is running, and takes the oldest first. Events are still not dropped and not reordered across documents; throughput on a single hot document is the cost, so it is opt-in. Without it the contract of §9.2 stands.

## 13. Operating triggers: status, gaps and rescan (issue 27.8)

- **`backd triggers status`** (and `GET /_admin/triggers/status`): the lease holder and epoch, watermark age and lag, queued backlog per realm and trigger, and open gaps.
- **`backd triggers rescan --realm R --collection C [--trigger T] --updated-since TIME [--until TIME]`** (and `POST /_admin/triggers/rescan`): scans `C` for documents with `_meta.updated_at` in the range and enqueues a synthetic `updated` event for each, with `rescan: <run id>` in the input and the run id in the job key (so it doesn't collapse with originals — handlers are idempotent). Limits: it evaluates `when` against `document` only, so triggers whose `when` needs `before` or `changed()` are listed as not rescannable; deletions can't be rescanned; a range scan over `_meta.updated_at` is a collection scan unless the collection indexes it, and the command says so and requires `--confirm` over a size threshold.
- **Closing a gap** is explicit: `backd triggers gaps resolve <id>`; unresolved gaps appear in `status` and in a metric.

## 14. System events: named aliases only (issue 27.9)

The dispatcher's stream includes the projected system collections (§4). Built-in events are registered as internal triggers on it, so one dispatch path can serve app collections and backd's own events:

| realm.yaml (unchanged) | Built-in trigger | Input (unchanged) |
|---|---|---|
| `account.on_signup` | `created` on users | today's `{user_id, provider, profile}` |
| `organizations.hooks.on_member_joined` | `created` on memberships | today's event with `seq` |
| `organizations.hooks.on_member_left` | `removed` on memberships | today's event with `reason` |
| `organizations.hooks.on_roles_changed` | `updated` on memberships, and team changes, where computed access differs | today's before/after `MemberAccess` |

**Migration is gated by parity, event by event.** The legacy in-process paths (`queueOnSignup`, `queueOrgHook`/`memberChanged`/`teamChanged`) stay the live path until the parity tests for that event pass, and are removed only in the commit that proves it. Known obstacles the issue must resolve, or keep the legacy path for:

- **`on_roles_changed`** also fires from team role changes and windows, which change effective access without a per-member document change; the "before" is computed in-process today (`teamChanged`). A stream-side computation must reproduce it, concurrent changes included.
- **`seq`** is allocated at write time today; replays must produce the same value, so it has to be derived deterministically, not allocated at enqueue.
- **The signup `profile`** comes from the provider flow and is probably not on the user document, so the stream can't rebuild it.

Where an event can't be reconstructed exactly, it keeps an enqueue **inside the write's MongoDB transaction** (job inserted with the document) — no loss, same input, no stream involved. Either way the realm.yaml keys and the function inputs do not change, and the stable aliases are the whole public surface.

**Why aliases and not a `source:` knob.** Users cannot declare triggers on system sources:

- The semantic events aren't field-level (`on_roles_changed` is a computed diff; `reason` is derived).
- System collections have no declared schema, so `when:` would be unchecked.
- A free `source:` would expose sessions, tokens, jobs, secrets metadata and consents to any realm developer who can edit collections.
- Internal layouts may change; the named events are the stable API.

**Extension path:** new named events added deliberately (`account.on_session_ended`, `organizations.on_org_created`), each designed like `on_member_left` was. `account.on_delete` remains deferred (account-lifecycle §13): *blocking, before the erasure policies* is a different shape from a reactive trigger.

## 15. What stays separate

- **Template data hooks** (`email.data_hook`, `pages.data_hook`): synchronous, in the render path, bounded at 2 s and 16 KiB, failures fall back. Triggers react to events; data hooks answer renders.
- **`email.function` delivery:** a named function backd calls with an input/answer contract, not a data event.
- **Cron:** a schedule, not an event.

## 16. Testing helpers, client and Shelf (issues 27.10, 27.11)

No client runtime surface — triggers are configuration.

- **`@backd/functions-testing`** gains `fakeTrigger({ event, before, after, by?, fields? })` producing the §10 envelope (with `changed` computed), so handlers are unit-testable without a replica set.
- **Typegen** emits the trigger input type per collection.
- **Client docs:** what triggers are, that `admin.invokeFunction` runs the function (not the trigger), and the dedup-on-`event.id` pattern.

**Shelf:** the pipeline chapter walks commit → PR → merged: three triggers on one `status` field with transition `when`, `ordered: true` and `by` in action, a deliberately chained `max_chain` demonstration, and debugging with `backd functions history`, `backd triggers status` and `_meta.chain`.

# Part III — Realtime

Clients receive changes as they happen instead of polling: a list that updates itself, a document that refreshes when someone else edits it.

- **Change feeds, not live queries:** a subscription watches a filtered collection or one document. Events say what was created, updated or removed **for that subscriber**. The client keeps its own sorted list.
- **Rules for every subscriber, every event:** the same compiled rules as the API, evaluated in memory. `removed` is never sent to someone who couldn't see the document before.
- **Immediate revocation:** access changes reach open streams at once.
- **Plain HTTP:** Server-Sent Events read with `fetch`, sessions in headers only.
- **Bounded cost:** opt-in collections, limits, slow-client handling, a memory budget, metrics and a cost guide.

## 17. Configuration and protocol (issue 27.13)

```yaml
# collection.yaml
realtime: true                # this collection can be subscribed to
```

A realm can lower the limits in `realm.yaml` under `realtime:`.

| Setting | Default |
|---|---|
| `BACKD_REALTIME_MAX_SUBSCRIPTIONS_PER_STREAM` | 20 |
| `BACKD_REALTIME_MAX_STREAMS_PER_USER` | 5 |
| `BACKD_REALTIME_MAX_STREAMS` (per instance) | 10,000 |
| `BACKD_REALTIME_BUFFER` (per stream) | 256 KiB or 1,000 events |
| `BACKD_REALTIME_MEMORY` (per instance, all buffers) | provisional; set from 27.20 |
| `BACKD_REALTIME_HEARTBEAT` | 25s |

### 17.1 Opening a stream

```
POST /v1/{realm}/_realtime
Authorization: Bearer <session>      (optional: anonymous streams follow read rules)
Content-Type: application/json

{
  "subscriptions": [
    { "id": "s1", "db": "main", "collection": "items", "where": { "_meta.org": "o_1" } },
    { "id": "s2", "db": "main", "collection": "items", "doc": "i_42" }
  ],
  "resume_from": "<token>"            // optional
}
```

- **Response:** `200` with `Content-Type: text/event-stream`, read with `fetch` streaming. `EventSource` isn't used because it can't send headers, and sessions never go in URLs.
- **Validation:** subscriptions are validated like list queries (allowlisted `where`, collection opted in, limits). Errors answer before the stream starts: `400 realtime_not_enabled`, `400 too_many_subscriptions`, `400 invalid_query`, `429`.
- **What a subscription can't do:** `where` goes through `ParseWhere` — so `$search` is already out — and is further restricted to the operators the in-memory matcher supports (comparisons, `in`, `is null`, `between`); `$like`/`$ilike` and anything else answer `invalid_query` at subscribe time. `order_by`, `limit`, `page` and `expand` don't exist in a feed — ordering and loading are the client's job (§21).
- **Changing what's watched:** opens a new stream with the previous `resume_from`; the JS client does it transparently. The token is not bound to the subscription set (§17.3): events replayed for a subscription added in between are reconciled by version.

### 17.2 Events

```
event: ready
data: {"subscriptions":["s1","s2"]}

id: <resume token>
event: created
data: {"sub":"s1","doc":{ … as the subscriber would read it … }}

id: <resume token>
event: updated
data: {"sub":"s1","doc":{ … }}

id: <resume token>
event: removed
data: {"sub":"s1","id":"i_7","version":12}

event: reset
data: {"subs":["s1"],"reason":"access_changed"}

event: session_ended
event: reauth

: heartbeat
```

- **Documents** in `created` and `updated` are exactly what a read would return for that subscriber: rules applied, `_meta.version` included, no `_expanded`.
- **`removed`** carries the id and the last version — for deletes *and* for updates that made the document invisible to this subscriber (§18).
- **`reset` reasons:** `history_expired`, `access_changed`, `slow_consumer`, `preimage_missing`, `config_changed` — a realm reload touching what a subscription's visibility depends on (the collection's rules or its `realtime:` opt-in); a collection losing `realtime: true` resets all its subscriptions, and re-subscribing then fails with `realtime_not_enabled`.
- **`session_ended`** closes the stream; the client signs in again.
- **`reauth`** closes the stream at access-token expiry; the client reconnects with a fresh token and its resume token.

### 17.3 Starting state and resuming

1. The client opens the stream and waits for `ready`.
2. It loads each subscription's data with normal list or get calls, buffering events received meanwhile.
3. It applies the buffered events, then live ones, **ignoring any event whose version isn't newer** than what it holds. Replays and races are harmless.

**Resuming:**
- each event's SSE `id` is a resume token: MongoDB's change-stream token plus the realm, the principal (session or key subject) and the issue time, **signed** with a per-realm key stored in the system database — so any instance can verify it after a reconnect through a load balancer, and it can't be forged or presented by someone else;
- reconnecting with `resume_from` (or `Last-Event-ID`) continues exactly after it;
- replayed events are evaluated with the subscriber's **current** access, never the access at the time, so a resume can't reveal more than a list would;
- if the change history no longer reaches it, the affected subscriptions get `reset` (`history_expired`) and the client reloads them.

**History window:** the change-stream retention window — the MongoDB oplog size.

## 18. Rules and visibility per subscriber (issue 27.14)

For each change on a `realtime: true` collection with subscribers on the instance:

1. **Once per change:** decode (§3), read the post-change document and the pre-change image, and find candidate subscriptions (§18.1).
2. **For each candidate subscriber,** evaluate in memory with their loaded access:
   - `match_after` = `where` and the `read` rule on the new document (false for deletes);
   - `match_before` = `where` and the `read` rule on the pre-change image (false for creates).
3. **Send:**
   - `created` if not before and after (inserts, and updates that made the document visible);
   - `updated` if before and after;
   - `removed` if before and not after;
   - nothing if neither, so strangers learn nothing.
4. **Render the document** as the subscriber's read would. Subscribers seeing the same version share one encoded payload.

**The matcher is pinned:** everything evaluated per change — the subscription's `where` and the collection's `read` rule — must stay inside the operators `storage.Match` implements (`eq`, `ne`, `in`, `nin`, `isNull`, `between`, and the comparisons, with MongoDB's array and missing-field semantics). `where` violations are refused at subscribe time; a `realtime: true` collection whose `read` rule compiles outside the subset fails at startup. `storage.Match` is the reference implementation of the differential tests, so the subset is verified before it carries production traffic (27.14).

**Missing pre-image fails closed — with a reset.** If a change arrives without its pre-image, `match_before` can't be computed. Sending `removed` would tell subscribers who never saw the document that its id exists, and guessing could leave a document visible to someone who lost access. Instead, the collection's subscriptions get `reset` (`preimage_missing`), coalesced and rate-limited per collection so a retention misconfiguration is a trickle of reloads, not a storm. A reload costs bandwidth and reveals nothing.

**Testing:** the pushdown property tests gain a realtime comparison. For generated documents, callers and changes, the events produced must match what list queries before and after the change would show.

### 18.1 Candidate subscriptions

Checking every subscriber of a busy collection on every change doesn't scale, so subscriptions are indexed:

- **Document subscriptions:** a map lookup on the id.
- **Equality keys come from the resolved filter, not the rule text:** the parsed `where` AND-ed with `Rule.Filter(v)`'s result is a `storage.Filter`; `Condition`s with `OpEq`/`OpIn` on a field under `And` are indexable keys. `inOrg`, `hasOrgRole`, `inTeam`, `hasTeamRole` already compile to `_meta.org`/`_meta.team` `in […]` conditions per subscriber, so they index like plain equalities.
- **One group per subscription:** the key with the best expected selectivity (`_meta.org`, then `_meta.team`, then other fields) places the subscription; a change is only checked against groups matching its values, before and after. The index is a shortcut — the full filter still decides every candidate.
- **The rest:** subscriptions whose key sits under `Or`/`Not`, or with no usable key, are checked one by one, and metrics show how many.

**The cost guide** explains that subscribing with an equality on a grouping field keeps checks to a handful per change. Without one, checks per change equal the number of subscribers on that collection on that instance.

## 19. Access changes (issue 27.15)

- **At open:** the stream loads the subscriber's access: session (with MFA flag and methods), realm roles, organization memberships with team roles, claims, `require_mfa` per organization, and consents where rules use them.
- **Watching system data:** the realtime consumer's system watch (§4, projected) covers sessions, users' roles, memberships, teams, organizations, consents and API keys. For connected subscribers affected by a change:

| Change | Effect |
|---|---|
| session revoked, signed out, user deleted | `session_ended`, stream closed |
| API key revoked or expired | `session_ended`, stream closed — reconnecting re-authenticates and fails |
| API key nearing expiry | stream closes at `ExpiresAt`, like `reauth` does for token expiry |
| realm roles changed | access reloaded; `reset` (`access_changed`) **if the subscriber's effective access differs** and the collection's rules use roles |
| membership, team membership or team roles changed; organization `require_mfa` or claims changed | access reloaded; `reset` if effective access differs and the rules use organization helpers |
| consent withdrawn or accepted (if rules use `hasConsent`) | access reloaded; `reset` if effective access differs |

- **No reset storms:** an organization's access model is computed once per change, then each affected subscriber's access is diffed before/after; only a real difference resets. The JS client reloads reset subscriptions with random jitter.
- **Why `reset`:** these changes can make documents visible or hidden without the documents changing, so the client reloads those subscriptions.
- **API keys:** a `data` key bypasses per-document rules but not its scopes — a subscription on a collection the key can't read is refused at open; scopes are fixed at creation, so they never change mid-stream. A key acting as a user follows that user's rows above, while the key's own revocation still closes the stream. `_meta.team` writes need no system watch — a document joining a team is an ordinary `updated`/`created`/`removed` for whoever the rules now show it to.
- **Token expiry:** streams close with `reauth` at the access token's expiry, so no stream outlives its credentials.

## 20. Limits, slow clients and expansion (issues 27.13, 27.16)

- **Limits:** subscriptions per stream, streams per user, streams per instance, and one open per second per user. Anonymous streams are limited per IP.
- **Bounded buffers:** each stream has one (256 KiB by default) and the instance has a **memory budget** over all of them. When a client can't keep up — or the budget is exceeded, largest buffers first — the buffer is dropped, its subscriptions get `reset` (`slow_consumer`) and the client reloads, so memory stays bounded and one slow client never stalls the change stream for others.
- **Opening is cheap** (no queries), and the list loads that follow are normal, rate-limited API calls.
- **Events never include `_expanded`.** The JS client keeps referenced documents in a cache by id; when events bring ids it doesn't have, it fetches them in one batched read with field selection, under normal rules. Referenced documents update in the UI only if the app also subscribes to them, and the docs show the pattern.

## 21. JavaScript client (issue 27.16)

```js
const live = client.realtime.subscribe([
  { collection: "items", where: { "_meta.org": orgId }, load: { sort: "-_meta.created_at", limit: 50 } },
]);
live.on("change", (state) => render(state.items));   // sorted, reconciled list
```

- **Behind the scenes:** one stream per client with multiplexed subscriptions; stream-first loading, buffering and version reconciliation; automatic resume, `reset` reloads (with jitter), token refresh on `reauth`, and reopening when subscriptions change.
- **`load`:** the client's own initial list call. Sorting and limits are applied on the client, and new documents are inserted in order.
- **References:** an optional reference cache, `references: { author: { collection: "profiles", fields: ["name"] } }`.
- **Errors** are typed, and status is reported (`connecting`, `live`, `resyncing`).

## 22. Realtime: Shelf (issue 27.17)

- **Live items:** the team's items list becomes live. A member adding an item shows up for others at once, and a member removed from the team sees the list reset and lose access immediately.
- **Comparison:** the chapter compares a subscription filtered by `_meta.org` (grouped, cheap) with an unfiltered one, using the metrics.

# Merged tail

## 23. Metrics

**Consumers** (per instance): stream lag, re-opens, watched-namespace count, open/idle state, filtered events, memory budget in use.

**Triggers** (per realm, collection and trigger): events seen and matched; jobs enqueued; dedup hits; drops by reason (`chain`, `burst`); gaps by reason (`window`, `backlog`, `preimage_missing`); backlog; `when` cost per change; dispatch lag and watermark age; lease holder, epoch and handoffs.

**Realtime** (per realm and collection): open streams and subscriptions; events sent by type; checks per change (with the share from groups); time spent per change; resets by reason; buffer drops.

## 24. Security considerations

**Shared**

- **System secrets never enter the feeds:** system collections are projected server-side to the minimum fields and events are never logged (§4).
- **No degraded mode** without pre-change images; pre-images follow provisioning, and disabling is explicit and irreversible (§5).
- **Missing images fail closed on both sides, differently:** realtime resets (never `removed`, which would leak ids); triggers skip, meter and record a gap (§9.3, §18).
- **Replay window = change-stream retention** on both sides; the docs point at both sizing knobs.
- **Privilege is stated:** the MongoDB role needed for a deployment-wide stream is documented and probed at startup.
- **Lease holder is trusted:** any `BACKD_TRIGGERS=true` instance can become dispatcher; fenced by epoch, so a stale holder can't move the watermark.

**Triggers**

- **No HTTP surface:** dispatch is internal; the functions are `internal: true`.
- **Startup-checked surface:** trigger functions must exist, be internal and async; `when:` fields are schema-checked.
- **Admin by declaration, visible:** `ctx.admin.db` is always present in a trigger run and `backd triggers status` lists it. A trigger function is the realm's most privileged code, reviewed like `admin: true` config.
- **`by` is attestable, not authoritative** — and never the erase sentinel.
- **Bounded, minimal jobs:** no bodies unless `fields:` asks; the job-input cap.
- **Loops bounded:** chain depth, chained burst cap, dedup on the job key.
- **Overload is visible:** backlog ceiling per realm, gaps recorded, no silent drop.
- **System events are realm-owned vocabulary** (§14): no user-declarable system sources.

**Realtime**

- **Rules on every event:** the same rules as reads; `removed` only for previous viewers; replays evaluated with current access.
- **Sessions:** only in `Authorization`. Streams close at token expiry and on revocation — API keys included.
- **Signed resume tokens:** per-realm key, bound to realm and principal.
- **Bounded resources:** limits, opt-in collections, per-stream buffers, an instance memory budget and anonymous limits per IP.
- **Load and soak tests (27.19, 27.20)** before each feature's security review.

## 25. Documentation (issues 27.12, 27.18)

- **`collection.yaml` reference:** `triggers:` (keys, caps, `when` scope, `fields`, `session`, `ordered`) and `realtime:`.
- **Functions guide, "Triggers" page:** the idea; the input contract; identity (`ctx.admin.db`, `by`); idempotent handlers and dedup on `event.id`; the transition-`when` pattern; chains and `max_chain`; ordering; debugging via `backd functions history`, `backd triggers status` and `_meta.chain`.
- **Realtime concepts:** change feeds versus live queries, events, starting state and resuming, what `reset` means and how to handle it.
- **Cost guide:** the cost model of §6 as written; `when`/`where` evaluation per change; hot collections and chained caps; `fields:` to keep jobs small; designing subscription filters with an equality on a grouping field; pre-image storage; reading the metrics.
- **Migration note:** `account.on_signup` and `organizations.hooks.*` — same config, same inputs; which events run on the dispatcher and which keep the transactional path.
- **Operations:** `BACKD_EVENTS`/`BACKD_TRIGGERS`, dedicated instances and proxy routing (SSE buffering and idle timeouts), triggers needing a running worker, restores invalidating the stored position, the lease, the MongoDB role (`readAnyDatabase` or equivalent), `backd provision` and the pre-image lifecycle, resume-window sizing (oplog), gaps and `backd triggers status`/`rescan`, alerting on `trigger_window_missed`, backlog and `trigger_missing_preimage`, MongoDB 6.0+ and pre-change images.
- **Limits and errors** with what to do; OpenAPI for `/_realtime` (27.13) and the admin triggers routes (27.8); the JS client guide with the reference cache pattern.

## 26. Issues

| id | Issue | Depends on |
|---|---|---|
| 27.1 | Change feed library, consumers and measurement gate | — |
| 27.2 | Pre-image provisioning and requirements | 27.1 |
| 27.3 | Trigger configuration and `when:` | 27.2 |
| 27.4 | Dispatcher: lease, durable stream, watermark, enqueue | 27.3 |
| 27.5 | Trigger execution and the function contract | 27.4 |
| 27.6 | Chains, loop bounds and backpressure | 27.5 |
| 27.7 | Ordered triggers | 27.5 |
| 27.8 | Trigger operations: status, gaps and rescan | 27.4 |
| 27.9 | System-event aliases on the dispatcher | 27.6 |
| 27.10 | Trigger testing helpers and client docs | 27.5 |
| 27.11 | Shelf pipeline chapter | 27.10 |
| 27.12 | Trigger documentation | 27.11, 27.8 |
| 27.13 | Realtime streams and subscriptions | 27.2 |
| 27.14 | Rules per subscriber | 27.13, 14.2 |
| 27.15 | Access changes on open streams | 27.14, 13.1, 14.4, 14.7 |
| 27.16 | Realtime in the JavaScript client | 27.15, 15.4 |
| 27.17 | Shelf live items | 27.16, 14.10 |
| 27.18 | Realtime documentation | 27.17 |
| 27.19 | Trigger load and soak tests | 27.6, 27.8 |
| 27.20 | Realtime load and soak tests | 27.15 |
| 27.21 | Trigger security review | 27.12, 27.19 |
| 27.22 | Realtime security review | 27.18, 27.20 |

Supersedes milestone `18-realtime` (18.1–18.9, GitHub #369–#377), the unuploaded `26-triggers` (26.1–26.9) and backlog item 7.21 (GitHub #45).

## 27. Alternatives considered

**Shared**

- **Oplog tailing instead of `watch()`:** rejected — not portable, no resume tokens, no pre-change images.
- **One change stream per realm:** impossible — `watch()` scopes are collection, database or deployment, and a realm spans several databases.
- **One physical stream shared by both consumers:** rejected — every instance would decode all trigger collections, a realtime subscriber would re-open the dispatcher's stream, and positions of different durability would be coupled. The library is shared, the streams are not.
- **A database-scoped stream now:** deferred — N scans per consumer, more moving parts; deployment scope with a documented role is the first version.
- **A watched set driven by subscribers:** rejected — re-opens churn the stream with gap risk for little saving.
- **Enabling and disabling pre-images at startup:** rejected — disabling is irreversible and breaks rolling deploys and `verify` mode.
- **An external broker or dedicated realtime role:** rejected; configuration covers dedicated instances.

**Triggers**

- **Enqueue at write time (in the request path):** rejected — a failed enqueue is logged and lost, writes block on queue health, no resume. (Kept only as a transactional enqueue for system events that can't be rebuilt from the stream, §14.)
- **Per-realm leases and watermarks:** rejected — the stream is deployment-wide; one lease and one watermark are simpler and enough.
- **Function-side declaration (`on:` in `function.yaml`):** rejected — a trigger is the collection's reactive contract.
- **Before-write or synchronous triggers:** rejected — every write would wait on the executor.
- **Exactly-once via a transactional outbox:** rejected — double writes on every change; at-least-once with dedup on a stable key, plus recorded gaps and rescan, covers the loss window explicitly instead.
- **Dropping first-party events above a rate:** rejected — an import would silently lose data; only chained events are rate-capped, and overload becomes backpressure and a per-realm recorded gap.
- **Loop lineage by system-record lookup on `request_id`:** rejected — a lookup per change; stamping `_meta.chain` at the write boundary needs none.
- **Full documents in the payload by default:** rejected — jobs outlive erasure; bodies are opt-in via `fields:`.
- **Per-document `seq` counters instead of `version`:** rejected — `_meta.version` exists.
- **User-declarable system sources (`source:`):** rejected — named events are the reviewed extension path.
- **`by` as a scoped `ctx.user`:** rejected — it would imply an authority the write can't prove.
- **Missing pre-image: evaluate `when` with `before` as `null`, or fire anyway:** rejected — skip, meter and record.

**Realtime**

- **Server-maintained live queries:** rejected — cost grows with subscribers times result sizes. **Notifications only:** rejected as polling with extra steps.
- **WebSocket:** rejected — custom protocol, upgrade-aware proxies, in-band authentication. **Long polling:** rejected for latency and request volume.
- **The server sending the initial result on the stream:** rejected — brings sorting and limits back. **No resume:** rejected — mobile clients would reload constantly.
- **Re-querying MongoDB per subscriber:** rejected as unscalable. **Access fixed at open:** rejected — breaks immediate revocation.
- **`removed` for a missing pre-image:** rejected — leaks ids; a coalesced reset instead.
- **A resume token bound to the subscription set:** rejected — conflicts with changing subscriptions and adds no safety, since replays use current access.
- **Resetting every subscriber on any access change:** rejected — storms; reset only on an actual difference.
- **Every collection watchable, or no slow-client handling:** rejected.
- **Expansion in events, or propagating referenced changes:** rejected.
- **Channels now, or presence only:** deferred.

## 28. Follow-ups

- `batch:` on triggers — an array of events per run, opt-in — for hot collections.
- A database-scoped consumer scope for shared clusters with restricted MongoDB users.
- **Field encryption** (a future milestone, no design obligations here): decrypt and `reveal` in both feeds, refusing or handling collections that declare encrypted fields, and keeping ciphertext and secrets out of job inputs. Whichever milestone lands second owns this integration and its review.
- **DocumentDB** (to be tried later): change-stream behavior, pre-image support and retention differ; nothing about it is promised or documented until it is evaluated.
- Named system events beyond today's aliases (`account.on_session_ended`, `organizations.on_org_created`, `account.on_delete` when erasure needs it).
- Channels: ephemeral messages, presence and broadcast with join and send rules, as their own design — may add `send`/`join`-style triggers.
- Push notifications for offline users — a trigger can already serve it via `ctx.push`.
- **App configuration live updates (22.6):** a `config` subscription kind on the realtime stream, filtered by the configuration's per-path read rules; defined together with 22.6, on the protocol of §17.
- Admin UI: live views in the data browser; trigger health per collection (matched/enqueued/dropped, lag, gaps).
