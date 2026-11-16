# CI failures and flaky tests

Status: done, 2026-11-16 (issue #1059 and #1061 to #1065). The window is the 15 days before it; the
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
| 1 / 2 | `test`: `TestWindowMissedIsRecordedAndReadingContinues` | test | The stored position was already `"0"`, so waiting for it said nothing, and the test added its change before the stream reopened "from now". Reproduced with a slow reopen; waits for the second open now (#1065) |
| 1 / 0 | `test`: `TestConsumerContinuesAfterSetChange` | test | Waited one second "for the consumer to open" before writing; a stream opens from now, so a slow open lost the write. Waits for the consumer's `Idle` (it opened) now (#1065) |
| 0 / 6 | `production-reference`: "backd refuses to start when MongoDB differs from the config" | not flaky | Only on the branches `cli-8` to `cli-10`, during the CLI/server split, and fixed by later commits (#1065) |
| 1 / 0 | `test`: image pulls from the registry | infrastructure | Docker Hub 502 on the dockerized test; rerun |
| 1 / 1 | `generated-types`, `supply-chain`: Go module proxy | infrastructure | `read "https://proxy.golang.org/…zip"` fails; the download has no retry |

**A correction.** The first version of the survey matched the names of passing Playwright tests
too (their `✓` lines), so it showed a suite of failing admin UI tests and `files.spec.ts › unsaved
edits survive a file change`; that test never failed. Only the `✘` lines are failures now. The
admin UI test that did fail at random is `checks.spec.ts › a collection is checked against its
schema` (4 attempts in 15 days, on `main` too), and it was a server race (below).

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
4. **A real bug, found by a flaky UI test (#1065).** `GET /_admin/data-checks` asked for the
   running checks and then for the queued ones; a job a worker claimed between the two questions
   was in neither, so the admin UI showed "nothing running", stopped polling and never showed the
   report (30 s timeout; the CI retry then failed too, because the first attempt had left a
   report: the test cannot be repeated). One question now, with a test that moves the job between
   the two reads.
5. **A test passed on re-run is a result nobody sees.** The run is green, and the first attempt
   disappears from the list of failures. CI should report a job that needed an attempt more
   than once, and Playwright's own retry must not make a flaky test green: `failOnFlakyTests` is
   on in CI (#1065).

## What is not flaky

Lint failures, type errors, invalid configurations in fixtures, `unknown strategy` in the
Shelf tour and the Trivy findings were real: they failed again on the next attempt or were
fixed by a commit.

## What was done

| Finding | Issue | Result |
|---|---|---|
| `ready` before the change stream was open | #1061 | Fixed in the product (`Lazy.WaitOpen`, `Hub.Open` waits; `503` when the database does not accept the stream). A Go test with the real change stream creates a document right after Open 200 times; it fails on the second run without the fix |
| Parallel `deno bundle` share one cache | #1062 | `runDeno` tries again, up to three times, only for that message |
| Infrastructure failures need a retry | #1063 | `scripts/retry.sh`: only for known infrastructure messages; the production stack prints why it did not come up |
| A retried pass is invisible | #1064 | Daily **Flaky tests** workflow opens one issue per failure (label `flaky-test`); the pull request template asks for it |
| The unexplained ones | #1065 | The schema-check listing race (a real bug), `TestWindowMissed…`, `TestConsumerContinuesAfterSetChange`; Playwright `failOnFlakyTests`; the production refusal check was not flaky |

The repaired tests run 20 times in every CI run (`make test-repeat`, which lists them in
`FLAKY_TESTS`) and 200 times locally. Add a test to that list when you fix a flaky one.

## Still open

- Nothing known. The daily report opens an issue for the next failure that passes on a re-run;
  read it with `scripts/ci-failures.py --days 15`.
