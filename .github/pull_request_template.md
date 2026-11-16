<!-- What changes, and why. "Closes #N" for the issue it finishes. -->

## Checklist

- [ ] Tests and lint pass locally (`make test`, `golangci-lint run`); docs updated in the same change; `api/openapi.yaml` too if the HTTP API changed.
- [ ] No check was re-run to make it green. A check that failed and passed on a re-run is **reported before merging**: it is listed in an issue labelled `flaky-test` (the daily workflow opens them, or open it by hand with the failing run). Tests are the source of truth: a randomly failing test is a defect, not noise.
