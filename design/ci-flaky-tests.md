# CI failures and flaky tests

Status: first survey, 2026-11-16 (issue #1059). The window is the 15 days before it; the
period had an unusual amount of work pushed, so numbers are higher than usual.

Reproduce with `scripts/ci-failures.py --days 15` (needs the authenticated `gh`). It reads the
failed attempts of every workflow run, including the first attempt of a run that was re-run,
and the log of each failed job. **retried** means a later attempt of the same run passed, so
the commit did not cause the failure; **other** are failures nobody re-ran: real, or not yet
explained.

## What the 15 days look like

- 2904 workflow runs, 157 ended failed, 59 were re-run (a re-run is the usual answer to a red
  check), 253 failed job attempts.
- Most failed attempts are **real**: lint, a TypeScript error in the admin UI after widening a
  type, `unknown strategy` in the Shelf tour after strategies, a test fixture with an invalid
  `realm.yaml`. They are not flaky and not listed below.

## What failed and passed on a re-run

| Failures (retried / not retried) | Job and what | Kind | Cause |
|---|---|---|---|
| 19 / 4 | `admin-ui-e2e`: `apt` fails fetching Playwright's system packages | infrastructure | An Ubuntu mirror answers errors or truncates; the step has no retry |
| 12 / 8 | `shelf-flutter`: `apt` fails in "Linux build toolchain" | infrastructure | The same mirror failure, on the Flutter packages |
| 18 / 6 | `production-reference`: the stack did not come up | infrastructure, to confirm | The log of the early attempts stops at "building and starting the stack": an image build or pull failed before any check ran; the job prints nothing about why |
| 6 / 6 | `integration` (Python): `test_a_live_view_delivers_a_new_document[sync]` | **product race** | `ready` is sent before the change stream is open (below) |
| 3 / 1 | `test`: `TestHandoffContinuesFromTheStoredPosition` | test | Asserted the stored position right after the queue length; the position is stored after the job is queued. Fixed in `11f66ebf` (waits for it); no failure after that |
| 1 / 3 | `test`: `TestAsyncEndToEnd`, `TestRunConfig` | test infrastructure | Several `deno bundle` runs at the same time share one `DENO_DIR`; they race while installing the esbuild binary: `failed to copy esbuild binary … No such file or directory`. Seen on `main` too |
| 1 / 2 | `test`: `TestWindowMissedIsRecordedAndReadingContinues` | test, to read | Waits 5 s for "reading to continue from now"; under load that is not enough, or the wait is on the wrong event |
| 1 / 0 | `test`: `TestConsumerContinuesAfterSetChange` | test, to read | Single occurrence |
| 0 / 6 | `production-reference`: "backd refuses to start when MongoDB differs from the config" | to read | Only on the branches that changed the set of system collections; may be real |
| 1 / 0 | `test`: image pulls from the registry | infrastructure | Docker Hub 502 on the dockerized test; rerun |
| 1 / 1 | `generated-types`, `supply-chain`: Go module proxy | infrastructure | `read "https://proxy.golang.org/…zip"` fails; the download has no retry |

Admin UI Playwright failures come in whole-suite batches (every test in one attempt), which is
one cause per attempt (the stack or the package install), not many flaky tests. The one
individual test that showed up is `files.spec.ts › unsaved edits survive a file change`
(2 retried, 3 not): to read.

## Findings

1. **A real bug, found by a flaky test.** `GET`/`POST /_realtime` writes the `ready` event as
   soon as the subscription is registered, but the change stream of an instance opens
   **in the background** when the first subscriber arrives (`changefeed.Lazy.Acquire` starts
   `Consumer.Run` in a goroutine, and the stream starts "from now" when MongoDB accepts the
   `watch`). A document created between `ready` and that moment is never delivered. The
   documented contract ("open the stream and wait for `ready`") does not hold for the first
   subscriber of an instance. The fix is in the product, not in the test: `ready` must wait
   until the stream is open (or the stream must start at the cluster time of the subscription).
2. **Parallel `deno bundle` runs share `DENO_DIR`.** The Go tests that build functions need a
   `DENO_DIR` of their own per test process (or one warm-up before the parallel builds).
3. **Infrastructure failures need a retry with a limit**, not a human re-run: `apt-get` (with
   `-o Acquire::Retries=5`), `go mod download`, image pulls, and the production reference
   stack build. They must be recognizable in the log as infrastructure.
4. **A test passed on re-run is a result nobody sees.** The run is green, and the first attempt
   disappears from the list of failures. CI should report a job that needed an attempt more
   than once.

## What is not flaky

Lint failures, type errors, invalid configurations in fixtures, `unknown strategy` in the
Shelf tour and the Trivy findings were real: they failed again on the next attempt or were
fixed by a commit.

## Next steps (tracked in #1059 and its children)

- Finding 1: #1061. Finding 2: #1062. Finding 3: #1063. Finding 4: #1064.
- The ones marked "to read": #1065. Each fixed test runs 200 times locally and 20 times in CI.
