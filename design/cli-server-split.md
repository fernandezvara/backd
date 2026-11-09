# Design: two binaries, `backd` (CLI) and `backd-server`

- **Issues:** #845–#859 (milestone `40-cli-server-split`): #845 this design, #846–#855 the steps 1–10 of §9, #856–#859 step 11 (one per audit item)
- **Status:** draft, being iterated
- **Related:** [kubernetes.md](./kubernetes.md), [types-openapi.md](./types-openapi.md), [export-import.md](./export-import.md), `support_docs/code_refactor.md` (local audit, Phase 5)

## 1. The problem

One binary, `backd`, does everything: it is the developer's tool (templates, config checks, rules tests, types), the administrator's tool (orgs, keys, secrets, users, audit, storage), and every server role (API, realtime, admin, worker, executor, egress, provisioning). Consequences:

- **No visible line between who runs what.** `backd help` lists about 40 top-level commands in one flat list; a developer sees `egress` and `provision` next to `types`, and an operator sees `template project` next to `secret rotate-key`.
- **Everything links everything.** A developer installing the tool to run `backd types` gets the MongoDB driver, the HTTP API, the executor and the egress proxy compiled in.
- **The server wiring lives in `package main`.** `cmd/backd/main.go` (1,336 lines) holds the app, the roles, the startup checks and provisioning; `devreload.go`, `executor.go` and `egress.go` sit next to 30 command files. None of it can be tested or reused outside the command package.
- **Dev mode is unfriendly at the start.** A live reload bundles stale functions before checking them (`devreload.go:77`), but startup only checks them (`loadConfig` → `checkFunctions` → `reg.CheckBundles()`, `main.go:1051`): a fresh checkout or an edited function refuses to start until someone runs `backd functions build`. And any config error at startup exits the process, so the dev compose stack crash-loops instead of saying what is wrong.

## 2. The split

| | `backd` (CLI) | `backd-server` |
|---|---|---|
| **Who** | developers and administrators, on their own machine or in CI | the deployment: containers, pods, a service manager |
| **Touches** | CONFIG_DIR (read, and writes templates/types), the admin API over HTTPS, the local credentials file | MongoDB, CONFIG_DIR, storage, the executor, Deno (to bundle) |
| **Never touches** | MongoDB, Deno, the server's secrets | the developer's credentials file |
| **Ships as** | release binaries for Linux, macOS, Windows; also inside both images | both images; a Linux release binary for installs without containers |

Rule of thumb: **if it opens MongoDB or runs Deno, it is `backd-server`; everything else is `backd`.**

## 3. Where every command goes

### `backd-server`

| Command | Today | Why here |
|---|---|---|
| `serve` (`--with-worker`) | `backd serve` | API, realtime and admin listeners |
| `worker` | `backd worker` | async jobs, schedules |
| `executor` | `backd executor` | runs functions (Deno image) |
| `egress` | `backd egress` | the executor's only way out |
| `provision` | `backd provision` | collections, validators, indexes, pre-images |
| `functions build` | `backd functions build` | bundles with the image's own Deno, so the build always uses the exact version that runs it |
| `bootstrap` | `backd bootstrap` | creates the first admin straight in MongoDB, before anyone can log in |
| `secret rotate-key` | `backd secret rotate-key` | re-encrypts every secret with a new master key, in MongoDB |
| `user mfa-reset --local` | `backd user mfa-reset --local` | break-glass recovery of the only admin |
| `export --direct`, `import --direct` | `backd export\|import --direct` | the admin handler run in-process over MongoDB, no server needed |
| `triggers rescan` | `backd triggers rescan` | reads a collection and queues jobs directly |

The direct forms lose their `--direct` flag: on `backd-server` they are always direct, and on `backd` they always go through the API. Two groups are split between the binaries, with the same subcommand names: `user mfa-reset` (API on `backd`; local, without `--local`, on `backd-server`) and `secret` (`rotate-key` on `backd-server`, the rest on `backd`). `backd-server` also has `version`.

### `backd`

Everything else, with the same names (no script breaks except the commands that moved):

- **Project (local, CONFIG_DIR only):** `template ...`, `config check|fingerprint`, `databases`, `rules test`, `types`, `openapi`, `functions types`.
- **Account:** `login`, `logout`, `whoami`, `account ...`.
- **Administration (admin API):** `orgs`, `apikey`, `secret set|create|rotate|list|delete`, `user ...`, `audit`, `storage`, `app-config`, `files`, `data`, `encrypt`, `consents`, `compliance`, `push`, `export`, `import`, `functions invoke|history|jobs|cancel|rerun|schedules|pause|resume|logs`, `triggers status|gaps|resolve`.
- **Local, on an export package:** `export-decrypt` (decrypts a package with its key; no server, no MongoDB).
- `version`.

`backd help` lists them in these sections, and each command's help says whether it needs a running server.

### CI

`config check` verifies bundles (`reg.CheckBundles()`) but never builds, so it needs no Deno. The generated project's workflow (`internal/templates/files/project/backd.yml`) already runs in the Deno image, which carries both binaries, and becomes: `deno test`, `backd-server functions build`, `backd config check`, `backd rules test`. `functions types` and `types` never run Deno and stay in the CLI.

## 4. Images and shipping

- **Two images, as now.** The slim image (no Deno) runs API, realtime, worker and egress in production; they only *check* bundles. The Deno image (today `backd-executor`) runs the executor, `backd-server functions build` in CI, and every service in the dev compose stack (dev mode bundles on change).
- **Both images carry both binaries**, so `kubectl exec … backd whoami` works for operators.
- **Entrypoints change, commands don't.** The slim image's `ENTRYPOINT ["/usr/local/bin/backd"]` + `CMD ["serve"]` becomes `backd-server` + `serve`; the Deno image's `["/usr/local/bin/backd", "executor"]` becomes `["/usr/local/bin/backd-server", "executor"]`. Compose files and the chart that only pass a role as the command keep working; the ones that set `entrypoint: ["/usr/local/bin/backd"]` (the generated project's compose does, three times) change to `backd-server`.
- **GoReleaser** (`.goreleaser.yaml`) publishes `backd` for Linux, macOS and Windows, and `backd-server` for Linux, with `checksums.txt` and an SPDX SBOM per archive, as today. The release binaries are not signed today (only the images are, keyless with cosign); this design doesn't change that. Homebrew, Scoop and other package managers are not in this design.
- **The version stamp** (`-X main.version=…`) is set in three places: `.goreleaser.yaml`, `Dockerfile` and `docker/executor/Dockerfile`. Each stamps both binaries, and the target moves to `internal/buildinfo` with the version step (§6a).
- **`.gitignore`** gets `/cmd/backd-server/backd-server` next to `/cmd/backd/backd`, so a local `go build` in either never gets committed.
- **One version for both.** They are released together. The server never states its version: the CLI sends its own, and only an authenticated admin response says whether the server is newer or older, and only when they differ (§6a). The CLI warns once per command; a skew is never refused, but a development CLI is refused by a released production server (no compatibility promise before a stable release).

## 5. Code layout

```
cmd/backd/            main + the CLI's command tree, nothing else
cmd/backd-server/     main + the server's command tree, nothing else
internal/cmdutil/     what both trees share: kli helpers (act, required, optional,
                      boolean, str, flag), user I/O, printJSON, exit codes
internal/server/      the app (today's main.go): settings, roles, startup checks,
                      provisioning, dev reload, executor and egress bootstrap
internal/cli/...      the CLI's commands, grouped by section (project, account,
                      admin), on pkg/client + internal/adminclient
```

- **Isolation is tested.** A test (like `pkg/client/deps_test.go`) fails if anything `cmd/backd` imports reaches `internal/mongodb`, `internal/httpapi`, `internal/executor`, `internal/egress`, `internal/changefeed`, `internal/realtime` or the MongoDB driver.
- **The CLI's API commands are already close.** They go through `target` (`cmd/backd/remote.go:172`), built on the public `pkg/client` and `internal/adminclient`. The project libraries they need are driver-free already: `registry`, `rules`, `rulestest`, `typegen`, `openapigen`, `templates`, `functions`, `exportpkg`, `fieldcrypt`, `adminclient` (checked with `go list -deps`).
- **Three shared libraries are not driver-free, and the CLI uses small pieces of them.** They have to be split before the isolation test can pass:

  | Package | Pulls in | What the CLI uses | Fix |
  |---|---|---|---|
  | `internal/auth` | the MongoDB driver, `changefeed` | `ParseScopes` (apikey), `DecodeKey`, `KeyFingerprint` (export-decrypt), `ExportKey`, `SecretCipher`, `Portable*` (import) | the key, cipher, scope and portable-format helpers move to a driver-free package (e.g. `internal/authcore`); `auth` keeps the stores and uses it |
  | `internal/settings` | `changefeed`, `realtime` | the limit parsers `config check` applies (`ImageMaxPixels`, `ExpandLimitsFrom`, `AggregateLimitsFrom`, `TriggerLimitsFrom`) | the limit types and parsers move to a driver-free package; `settings` keeps the server settings |
  | `internal/httpapi` | everything | two header names, `DevDurationHeader` and `DevLogsHeader` (functions invoke) | the constants move next to the other shared header names (e.g. `pkg/client` or a small shared package) |

- **The CLI builds and works on Linux, macOS and Windows.** It builds today (`GOOS=windows go build ./cmd/backd` passes), but on Windows it can't find its credentials file: `credentialsPath` (`cmd/backd/remote.go:47`) tries `BACKD_CREDENTIALS`, `XDG_CONFIG_HOME`, then `HOME`, and Windows usually sets none of them. The home directory comes from the standard library's `os.UserHomeDir()` (`$HOME` on Linux and macOS, `%USERPROFILE%` on Windows) instead of reading `HOME` directly, so the order is `BACKD_CREDENTIALS`, `$XDG_CONFIG_HOME/backd/credentials`, then `<home>/.config/backd/credentials`: the same path as today on Linux and macOS, and the same layout on Windows (`%USERPROFILE%\.config\backd\credentials`). No new dependency: `github.com/mitchellh/go-homedir`, the usual library for this, is archived (since 2024), and its extras (a `~` expander, a shell fallback when `HOME` is unset) aren't needed. CI cross-compiles the CLI for the three on every change, and a test covers the path resolution.
- **Tests keep running in-process.** Both trees keep the `run(args, getenv, stdin, stdout, stderr)` entry point. End-to-end tests that need both (log in with the CLI against a server started in the test) live in a package that imports both, not in either `cmd`.

### Cleanup in this milestone

The `cmd/backd` items of the duplication audit (Phase 5), since that code moves anyway. Re-check each against the current code first: A14 (target plumbing) is already almost done (one `newTarget` copy left).

- A15 request/error helpers on `target`.
- A16 `fetchPages[T]` and `followJob` for the pagination and job-following loops.
- A17 export/import atomic writes.
- A18 the command-specific cleanups (email lookups, secret flags, MFA loop, executor/egress `runServer`, `printJSON` outliers).

The `internal/mongodb`, `internal/httpapi` and `internal/registry` items stay a milestone of their own, after the split.

## 6. Dev mode (`BACKD_DEV=true backd-server serve`)

Dev mode stays an environment variable (configuration is env-only). What changes:

1. **Startup builds like a reload.** Stale or missing bundles are built before the config is checked, with the same builder the reload uses (one function, called from both). A fresh checkout starts with no `functions build` step.
2. **Startup never exits on a config or build error.** The server starts, keeps watching CONFIG_DIR, and the first save that loads brings everything up (the same path as a live reload).
3. **An error report.** What failed (config, rules, a function's build), where (file, line when known), the tool's output (Deno's error for a build), and what to do. Logs stay JSON, in dev as in production: the report is one log record with those as fields (`kind`, `file`, `line`, `output`, `hint`), the build output included as it is. One record per distinct error, not one per tick. The same report, formatted for reading, is on the API root (item 4).
4. **The API root shows the errors.** The dev landing page (`internal/httpapi/devindex.go`) already lists the errors of the last failed reload (`ReloadErrs`); it shows the full report instead, above the current config. While no config has ever loaded, the page shows only the report, every `/v1/...` route (data, auth and admin alike: no realm is known yet) answers `503` with the error code `config_not_loaded` and the report in the error body, and `/ready` is not ready. `/health` stays up, so the container is not restarted.
5. **Only `serve` builds.** With the API and the worker in separate containers, the worker never builds: in dev it starts and waits the same way, following CONFIG_DIR (`followConfig`, which already retries "until it loads") until the bundles the API built are there. So two processes never build the same project at once.
6. **What still exits in dev.** Waiting is for what the developer edits: the config, the rules, the functions and their build. Everything else exits at startup as in production, with its error: invalid environment settings, an address dev mode refuses, MongoDB unreachable or not a replica set, a port already in use, and no Deno to build with when the config has functions (the error says to run dev mode on the `backd-executor` image, which carries it).
7. **Readiness after the first load.** Once a config has loaded, a later change that fails keeps the previous config serving, as today: `/ready` stays ready and the report is on the landing page and in the admin config warnings (`reload_failed`).
8. **The admin UI before a config loads.** It is served, but every call it makes answers `503 config_not_loaded`; it shows that error's message (the report's summary) and links to the landing page, instead of a sign-in form that can't work.
9. **No more one-shot build services.** Every dev stack runs `functions build` as a compose service before `serve`, the workaround for startup not building: the generated project (`internal/templates/files/project/compose.yaml:110`) and the repository's `docker-compose.yml`, `docker-compose.dev.yml`, `docker-compose.js.yml`, `docker-compose.python.yml` and `docker-compose.dart.yml`. They are removed: `serve` builds, and a build error shows on the API root instead of stopping compose before backd starts. The services that wait on them today (`depends_on: functions-build: condition: service_completed_successfully`, e.g. `docker-compose.js.yml:106`) wait for backd's `/ready` instead, through a health check on the backd service; so do the scripts and client test suites that start these stacks, since `/health` now answers before the config has loaded. What doesn't run in dev mode keeps its build, as `backd-server functions build`: the `docker/egress-test` stack's service, and the production reference's image build (`deploy/production/app/Dockerfile`).
10. **Types follow.** Startup already writes `backd.d.ts` in dev (`main.go:312`); after a wait, the first config that loads writes them too.

Production is unchanged: an invalid config or a stale bundle exits at startup.

## 6a. Version skew, without exposing the version

The server must not say which version it runs: an unauthenticated version string tells an attacker which known vulnerabilities to try. So the check works like the types check (`internal/httpapi/types_stale.go`): the client says what it has, and the server only says whether it differs.

- **The CLI says what it is in its User-Agent:** `backd/<version>`. Nothing sets a User-Agent today (requests go out as Go's default `Go-http-client/…`); `pkg/client` gets an option for it and the CLI sets it. No new request header. Only a User-Agent whose first product token is exactly `backd` counts; the other clients (JS, Go, Python, Dart) and browsers (the admin UI) are never compared or refused.
- **What the version is.** Today it is `-X main.version=` at release build time, `dev` otherwise. That would make `go install github.com/fernandezvara/backd/cmd/backd@v0.9.7` a `dev` build, refused by every production server. So both binaries fall back to the module version from `runtime/debug.ReadBuildInfo` when no version was stamped; only a build from a working tree (`(devel)`) is `dev`. The version lives in one place both binaries read (e.g. `internal/buildinfo`).
- **Comparing.** Both sides are semantic versions (`0.9.7`, `0.10.0-rc.1`), compared by semver precedence. Equal means no header; anything else is `newer` or `older`.
- **The server answers** `X-Backd-Version-Skew: newer` (the server is newer than the CLI) or `older`, and only:
  - on `/_admin/` routes,
  - after the caller is authenticated (an API key or an admin session), so an anonymous probe gets nothing back,
  - when the versions differ.
- **Skew is never refused:** any other User-Agent, or an unparsable one, is answered as usual. The CLI prints one line on stderr per command, e.g. `warning: the server runs a newer backd than this CLI (0.9.7); update the CLI`.
- **A development CLI only talks to a development server.** A CLI built from source sends `backd/dev`. A server accepts it only when it is a dev build itself (version `dev`) or runs with `BACKD_DEV=true`. A released server in production refuses every request carrying `backd/dev`, on every route and before authentication, with `403` and the error code `dev_client_refused` ("this CLI is a development build: use a released backd against a released server"). This covers a developer running the released dev compose stack with a CLI built from source, and keeps an untested build away from production data.
- It is a guard against mistakes, not an access control: anyone can send another User-Agent, and the refusal only tells the caller that the server is not in development.
- Between a `dev` side and a dev server, versions are not compared (no skew warning).
- Not exposed through CORS (`corsExposeHeaders`): browsers have no use for it.
- **Where it is checked.** One middleware on the public and admin listeners, before authentication, does the refusal; the skew header is added after authentication by the admin router. The internal listener (function callbacks) is not involved. In `api/openapi.yaml` the `403 dev_client_refused` is documented once, as a response every operation can give, and the skew header on the admin operations; the error code goes in the docs' error reference.

Rejected: a plain `X-Backd-Version` on every response (fingerprinting), a request header of its own (the User-Agent already says who calls), the exact version in `whoami` (an extra request per command, or a cache that goes stale), and a check at login only (misses later upgrades).

## 6b. Docs and everything that names a command

- **74 files** outside the Go code name a moved command (`backd serve`, `backd provision`, `backd bootstrap`, `backd worker`, …): docs pages, examples, `deploy/`, `docker/`, `scripts/`, the `Makefile`, the workflows in `.github/` and the project templates (compose, README, AGENTS.md). All change in the step that moves the commands.
- **About 50 strings in the Go code** name one too: error messages, hints and help that tell the user to run `backd functions build`, `backd provision`, `backd bootstrap`, … (in `internal/auth`, `internal/executor`, `internal/functions`, `internal/httpapi`, `internal/mongodb` and `cmd/backd`). They change in the same step, and a test fails if a Go string names a moved command under `backd `.
- **The command reference** (`docs/content/docs/operate/administration/cli.md`) becomes two pages, one per binary, with the CLI's sections (Project, Account, Administration) and a line on each command saying whether it needs a server.
- **The environment variable reference** says which binary reads each variable. The CLI reads `CONFIG_DIR`, `BACKD_URL`, `BACKD_API_KEY`, `BACKD_CA_CERT`, `BACKD_CREDENTIALS` (else `XDG_CONFIG_HOME`, then the home directory: `HOME`, or `USERPROFILE` on Windows), and `config check` also applies the server's limit variables (`BACKD_IMAGE_MAX_PIXELS`, the expand, aggregate and trigger limits) so that a check matches the server it is meant for. `BACKD_DEV` and the other server settings mean nothing to the CLI; `DENO` moves to the server with `functions build`.
- **The upgrade notes** (Operations → Upgrading) list every moved command with its new form, and the image entrypoint change.
- **The security model page and `SECURITY.md`**: the server never discloses its version; a development CLI is refused by a released production server.

## 7. Decisions so far

- `backd-server` owns everything that opens MongoDB or runs Deno; the CLI uses only CONFIG_DIR and the admin API.
- The developer runs the server in dev mode; the CLI has no `dev` command. The compose stack stays (the executor needs the Deno image).
- `backd-server` owns bundling, inside its image, so the Deno version is the image's.
- Two images, as now, with their names (`backd` slim, `backd-executor` with Deno); both carry both binaries.
- Dev mode stays `BACKD_DEV`.
- In dev, startup builds and never exits on errors; the report, Deno's build output included, goes to the log as JSON fields (logs stay JSON) and, formatted, to the API root next to the realms, databases and functions.
- Thin mains; `internal/server`, `internal/cli/...`, `internal/cmdutil`; a test keeps the server packages out of the CLI.
- CLI command names stay; the help is grouped.
- The `cmd/backd` cleanup is in; the server-library cleanup is a later milestone.
- Breaking: the moved commands are gone from `backd` in one release, with a release-notes line naming each one's new form (no installed base; no aliases).
- The dev stacks lose their one-shot `functions-build` service; stacks not in dev mode keep it.
- The split starts after milestone 39 (Kubernetes on DigitalOcean) merges, and updates its chart, pipeline and docs like everything else.
- The generated project (`template project`): its compose services run `backd-server`; its README has the developer install the `backd` release binary on the host (one static file, no Deno), talking to the dev stack on localhost and writing types next to the editor, with `docker compose exec backd backd …` as the fallback.
- Version skew follows the types check (`X-Backd-Types` / `X-Backd-Types-Stale`): the CLI's User-Agent carries its version, the server flags a mismatch on authenticated admin responses only and never states its own version (§6a). A `backd/dev` CLI is refused by a released server outside dev mode (`403 dev_client_refused`).

When this design is accepted, `support_docs/decisions.md` gets a line for the split (the rule, the two images, dev mode waiting, the version rule) and `roadmap.md` points at milestone 40.

## 8. Open questions

- How the Kubernetes chart and pipeline (milestone 39) change: commands and image entrypoints (mechanical once 39 has merged).

## 9. Steps

Agreed order: refactor with no behaviour change first (1–4), then the dev-mode fixes on the current binary (5–6), then the split (7), then the CLI's own structure and polish (8–11). Each step builds, passes the tests and updates the docs.

1. **`internal/cmdutil`.** Move the kli helpers (`act`, `required`, `optional`, `boolean`, `str`, `flag`, user I/O, `printJSON`, exit codes) out of `cmd/backd`. No behaviour change.
2. **Driver-free shared libraries.** Split the pieces of `internal/auth`, `internal/settings` and `internal/httpapi` the CLI uses into driver-free packages (§5). No behaviour change.
3. **`internal/server`.** Move the app, the roles, the startup checks, provisioning, dev reload, and the executor and egress bootstrap out of `cmd/backd/main.go`; `cmd/backd` calls it. Still one binary. The end-to-end tests that need both a server and CLI commands move to their own package.
4. **One function bundling step.** The reload's "build stale bundles" becomes one function in `internal/server`, used by reload and (next step) startup.
5. **Dev startup builds and waits.** With `BACKD_DEV`, `serve` builds stale bundles at startup; `serve` and `worker` never exit on a config or build error (§6, 2 and 6), log the report, and the first valid save brings them up. The dev stacks' one-shot `functions-build` services are removed (§6, 9).
6. **Errors on the API root.** The dev landing page shows the report; `/v1/...` answers `503 config_not_loaded` with it while no config has loaded, and `/ready` is not ready (API change: `api/openapi.yaml`).
7. **`cmd/backd-server`.** The new binary takes the server roles and the direct commands (§3); `backd` loses them. Dockerfiles and entrypoints, GoReleaser, the compose files (dev, production reference, generated project), the chart and pipeline, and every file in §6b change in the same step. CI cross-compiles the CLI for Linux, macOS and Windows.
8. **The CLI's packages.** Commands move to `internal/cli/{project,account,admin}`; `backd help` is grouped, and each command's help says whether it needs a server. A group whose subcommands are of both kinds (`functions`: `types` is local, the rest remote) is defined once and takes its subcommands from both packages; the help marks each one.
9. **Isolation test.** Fails if the CLI reaches `internal/mongodb`, `internal/httpapi`, `internal/executor`, `internal/egress`, `internal/changefeed`, `internal/realtime` or the MongoDB driver.
10. **Version skew warning.** One version source for both binaries with the build-info fallback (`internal/buildinfo`); the CLI's User-Agent `backd/<version>` (a `pkg/client` option), `X-Backd-Version-Skew` on authenticated admin responses (§6a; `api/openapi.yaml` and the contract tests change with it); the CLI warns once per command; a released server outside dev mode refuses `backd/dev` (`403 dev_client_refused`). The security model page and `SECURITY.md` say that the server never discloses its version.
11. **CLI cleanup.** The audit's A15–A18, one small PR each, after re-checking them against the code.
