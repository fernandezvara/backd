---
title: "Development"
description: "Workflow, repository layout, tests and CI."
icon: "code"
weight: 900
toc: true
---

## Workflow

- Work happens in small steps. Every step ends with exactly one commit with a concise message.
- Every step updates this documentation site (`docs/`) in the same commit, so the docs always match the code.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/backd/` | The `backd` binary: subcommand dispatch |
| `internal/settings/` | Runtime settings from environment variables |
| `internal/registry/` | Loads and validates the `CONFIG_DIR` tree into the registry |
| `internal/jsonnum/` | JSON number normalization (int64 / float64) |
| `internal/httpapi/` | HTTP layer: router, middleware, error envelope, document, `/_auth` and `/_admin` handlers, CORS |
| `internal/auth/` | Users, sessions, API keys, invitations, roles, password hashing and login throttling (storage-neutral); `authtest/` has an in-memory store for tests |
| `internal/rules/` | Loads, checks and evaluates `rules.yaml`; turns read rules into storage filters |
| `internal/templates/` | Starter files for `backd template` |
| `internal/query/` | Parses and checks `where` / `order_by` / `count` against the schema |
| `internal/storage/` | Backend-neutral repository interface and query conditions |
| `internal/mongodb/` | MongoDB specifics: connection, `$jsonSchema` validator translation, provisioner (including realm system databases), repository, auth store |
| `api/openapi.yaml` | The [HTTP API contract](contract/) (OpenAPI 3.1) and its lint settings |
| `clients/js/` | The [JavaScript client](../clients/js/) (`backd-js`) |
| `docs/` | This documentation site (Hugo + hugodoks) |
| `examples/config/` | Sample `CONFIG_DIR` used by `docker-compose.yml`. `workshop/` is the functions cookbook: its code is what the Functions pages quote (through the `example-file` shortcode, which reads it from `examples/config/workshop`), and it is tested for real (Deno unit tests with `make functions-testing-test`, and `TestWorkshopExample`, which runs every function on MongoDB with a real executor and worker) |
| `docker-compose.yml` | Local stack on `https://localhost:8443`: nginx in front of `backd` + MongoDB, the docs site and the example app |
| `docker/` | Images and config for the local stack: the docs server (Hugo), nginx, and the certificate templates (`docker/cfd/`) |
| `docker/certs/` | Generated, gitignored: the local CA and nginx's certificate |
| `scripts/example.sh` | Runs the local stack for `make example` |
| `scripts/check-dashboards.py` | Checks the Grafana dashboards: valid, only documented metrics, a panel for every alert (and, with `--prometheus`, every query) |
| `scripts/example-ci.sh` | Starts its own local stack and runs every attack script against it, as CI does |
| `scripts/local-certs.sh` | Creates or renews the local CA and certificate with certsfor |
| `deploy/production/` | The [production reference deployment](../operations/production/) and its end-to-end test |
| `Dockerfile` | Multi-stage build into a distroless image |
| `docker-compose.test.yml` | Dockerized test environment (project `backd-test`) |
| `.github/workflows/ci.yml` | CI: vet, OpenAPI lint, dockerized tests, release configuration check, JavaScript client checks and integration tests, the production reference test, docs build |
| `.github/workflows/pages.yml` | On version tags, builds this documentation site, checks its links and callouts, and publishes it to GitHub Pages (see [Releasing](#releasing)) |
| `.github/workflows/publish-js.yml` | On `js-v*` tags, publishes the JavaScript client to npm (see [Releasing the JavaScript client](#releasing-the-javascript-client)) |
| `.github/workflows/release.yml`, `.goreleaser.yaml` | Releases on version tags: binaries, GitHub release, container image (see [Releasing](#releasing)) |
| `.github/dependabot.yml` | Weekly dependency updates (see [Supply chain](#supply-chain)) |
| `docker-compose.js.yml` | Stack for the JavaScript client's integration tests (project `backd-js-test`) |

## Testing

All automated tests run in a dockerized environment, both locally and in CI:

```sh
make test         # docker compose -f docker-compose.test.yml run --rm tests
make test-local   # plain `go test ./...` for tests without external services
make lint-api     # lint api/openapi.yaml (needs Node)
make js-test      # JavaScript client: type-check and unit tests (needs Node)
make js-package     # the JavaScript client packed and installed like an npm user gets it (Node and TypeScript)
make js-integration  # JavaScript client against backd + MongoDB in Docker
make example      # the local stack on https://localhost:8443: API, docs (live reload) and example apps behind nginx
make hack-expenses  # attack the expenses example on the running local stack
make hack-expenses-functions  # attack the expenses-with-functions example on the running local stack
make workshop-tour  # walk the workshop tour without a browser on the running local stack, checking each step
make example-attacks  # start a stack of its own and run every attack script against it (CI does this); stop `make example` first
```

Two differential tests guard against data leaks and failed writes, and run against the real MongoDB in `make test` and CI:

- `internal/mongodb/rules_diff_test.go` checks that read rules pushed down to MongoDB never leak or hide documents. For each read rule, it compares the documents the rule allows when evaluated in memory with what lists return and what single reads (`GET /{id}`, and the lookups of `PUT`, `PATCH` and `DELETE`) return. It does this for every caller: anonymous, users with and without roles, owners and members.
  - It runs on hundreds of generated documents, some ownerless, some with missing or null fields.
  - The rules exercise `&&`, `||` and `!` short-circuiting, `nil` guards, `in` on arrays, `hasRole`, and `now` against `_meta` timestamps.
  - It runs the same comparison for the read rule of every sample `rules.yaml` in the repository (`examples/config`, the template sample and the JavaScript client's test configs), on documents generated from its schema.
  - It pins how missing and null fields behave where the rule language can't decide (`nil > 3`).
  - A self-test breaks the filters on purpose (a lost negation, `&&` and `||` swapped, `!=` as `==`, and so on) and fails unless the comparison notices each break.
- `internal/mongodb/validator_diff_test.go` checks that every document the API's JSON Schema validation accepts is also accepted by the MongoDB validator. It covers tricky number spellings and `_meta` with and without ownership fields. It runs on a synthetic schema and on every sample `schema.json`, with documents generated from each schema, valid and invalid, including extra properties for `additionalProperties: false`.

New sample collections under those directories are picked up automatically.

`make test` builds the test container from `docker/test/Dockerfile` (Go plus the Deno version functions are bundled with, so the function build tests run), and starts a single-node MongoDB replica set (`mongo:8.2`) next to it, plus a standalone MongoDB used only to check that `backd` refuses one. Integration tests read `MONGO_TEST_URI` (and `MONGO_STANDALONE_TEST_URI`) and are skipped when it isn't set. Each test uses its own uniquely named realm and drops its databases afterwards.

To run the integration tests outside the compose setup:

```sh
docker run -d --name backd-test-mongo -p 27018:27017 docker.io/library/mongo:8.2 --replSet rs0
docker exec backd-test-mongo mongosh --quiet --eval \
  "rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'localhost:27018'}]})"
MONGO_TEST_URI='mongodb://localhost:27018/?replicaSet=rs0' go test ./...
```

{{< hint warning >}}
MongoDB 8.0 (and the current `latest` image) refuses to start on Linux kernel 6.19 or newer ([SERVER-121912](https://jira.mongodb.org/browse/SERVER-121912)). Use 8.2.
{{< /hint >}}

## Continuous integration

GitHub Actions runs on every push and pull request:

- `go vet`, a check of the compose files, the OpenAPI lint, the dockerized test suite, and a check of the release configuration;
- supply chain: `govulncheck`, and a Trivy scan of the image;
- the JavaScript client: type-check, unit tests and integration tests against `backd` and MongoDB;
- the [production reference](../operations/production/) test (`deploy/production/test.sh`);
- a build of this documentation site.

## Releasing

Releases are published by `.github/workflows/release.yml` when a version tag is pushed:

1. Make sure `main` is what you want to release: the release notes are generated by GitHub from the commits since the previous tag, so commit subjects should say what changed, and a breaking change should say so in the subject or body.
2. Bump the version where it is pinned: `grep -rn "0.X.0" .` finds the template files (`internal/templates/files/project`), `api/openapi.yaml`, `SECURITY.md`, the examples in the docs (`deploying.md`, `cron.md`, `getting-started.md`), the landing page (`docs/data/landing.yaml`) and the tutorial's `docs/static/tutorial/compose.yaml`; add an *Upgrading to* section to `docs/content/docs/operations/_index.md` and a bullet to the versions list in `docs/content/docs/_index.md`.
3. Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`. Edit the notes on the GitHub release afterwards if they need context.

The workflow then:

- builds the binaries with [GoReleaser](https://goreleaser.com/) (`.goreleaser.yaml`): Linux, macOS and Windows on amd64 and arm64, as archives with `LICENSE` and `README.md`, plus `checksums.txt`;
- adds an SPDX SBOM for each archive, made with Syft;
- creates the GitHub release with those files and generated notes;
- builds the image and scans it with Trivy, and stops if it finds a critical or high vulnerability that has a fix;
- builds and pushes the multi-arch container image `ghcr.io/fernandezvara/backd:vX.Y.Z` with Docker Buildx, plus `latest` for versions without a pre-release suffix (a tag such as `v1.0.0-rc.1` is a pre-release and doesn't move `latest`), with SBOM and provenance attestations;
- signs the image with cosign, keyless: the signature is tied to this repository's release workflow through GitHub's OIDC identity.

A second workflow, `pages.yml`, runs on the same tag and publishes this documentation site to GitHub Pages, so the site always shows the latest tagged version. It needs *Settings → Pages → Source: GitHub Actions* set once in the repository. To publish a documentation fix between releases, run it by hand: *Actions → pages → Run workflow*, on `main`.

The version reaches the binary through `-X main.version`, in both GoReleaser and the Dockerfile (`--build-arg VERSION`); local builds report `dev`.

`make release-check` validates the configuration and builds every archive into `dist/` without publishing. CI runs `goreleaser check` and checks that the next version has release notes.

### Releasing the JavaScript client

The client is published to npm as [`backd-js`](https://www.npmjs.com/package/backd-js) by `.github/workflows/publish-js.yml`, on its own tags and version, independent of the server's:

1. Change `version` in `clients/js/package.json` (semver; while it is 0.x, a breaking change bumps the minor) and merge it to `main`.
2. Tag and push: `git tag js-vX.Y.Z && git push origin js-vX.Y.Z`. The tag must match the version and point at a commit on `main`; a version with a suffix, `js-v0.3.0-rc.1`, is published under the `next` dist-tag and doesn't move `latest`.

The workflow type-checks, runs the unit tests, then runs `scripts/check-js-package.sh`: it packs the library, checks what the tarball holds (the library, its declarations, `README.md` and `LICENSE`; no tests or examples), installs the tarball into a fresh project and uses it from Node and from strict TypeScript. CI runs the same check on every push (`make js-package`). Then it publishes with a provenance attestation, so each version on npm links to the commit and workflow that built it.

**One-time setup, on npmjs.com.** Publishing uses [trusted publishing](https://docs.npmjs.com/trusted-publishers): the workflow proves who it is to npm with GitHub's OIDC identity, so no npm token is stored in the repository. In the package's *Settings → Trusted Publisher*, choose GitHub Actions and enter the owner `fernandezvara`, the repository `backd` and the workflow filename `publish-js.yml`. For the package, set *Publishing access* to require two-factor authentication and disallow tokens.

**Mistakes.** A published version can't be changed or reused. Fix it with a new version, and mark the bad one with `npm deprecate backd-js@X.Y.Z "reason, use X.Y.Z+1"`. npm allows unpublishing only in narrow cases (within 72 hours, or for packages almost nobody uses), and a version number is never reusable, so deprecating is the way to correct a release.

## Supply chain

- **Vulnerabilities in the code:** CI runs [`govulncheck`](https://go.dev/doc/security/vuln/) on every push. It fails only on vulnerabilities in code `backd` actually calls; those in unused parts of dependencies are reported, not fatal.
- **Vulnerabilities in the image:** CI builds the image and scans it with [Trivy](https://trivy.dev/) on every push, failing on critical or high findings that have a fix. Releases scan again before publishing.
- **Updates:** Dependabot (`.github/dependabot.yml`) opens grouped weekly pull requests for Go modules (the server and the docs theme), the JavaScript client's dev dependencies, GitHub Actions, and the Dockerfiles' base images. CI checks each one.
- **Releases** publish SBOMs and a signed image (see [Releasing](#releasing)).

## Building the docs

The documentation site is built with [Hugo](https://gohugo.io/) and the [hugodoks](https://github.com/fernandezvara/hugodoks) theme, built on Pico CSS.

**Requirements**

- The theme is imported as a Hugo module, so `docs/` has its own `go.mod`. Building needs Hugo ≥ 0.154, plus Go and git.
- `docs/` is a separate Go module, so it's excluded from the root `go test ./...`.

**Where things live**

| Path | Contents |
|---|---|
| `docs/hugo.toml` | Site and theme configuration |
| `docs/content/docs/` | Documentation pages |
| `docs/data/landing.yaml` | Landing page sections |
| `docs/layouts/shortcodes/` | The `live-example` shortcode (shown only in the local stack) |

**Front matter**

Each page needs `title`, `description` (shown in navigation and section lists), `icon` (a name from the theme's built-in icon set, such as `settings` or `lock`; unknown names show a generic document icon), `weight` and `toc: true`.

**Commands**

```sh
make docs-serve   # live preview at http://localhost:1313/backd/
make docs         # build into docs/public/
```
