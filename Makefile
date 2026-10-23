COMPOSE_TEST = docker compose -f docker-compose.test.yml

.PHONY: dev dev-examples dev-build dev-down dev-reset dev-logs dev-backd lint build ui ui-test ui-e2e test test-local perf vet lint-api openapi-clients shelf-types shelf-types-update js-test js-package js-integration dart-package dart-integration functions-testing-test docs docs-serve example prod-test egress-test hack-expenses hack-expenses-functions workshop-tour shelf-tour shelf-tour-ci example-attacks release-check

build:
	go build -o bin/backd ./cmd/backd

# Full test suite in the dockerized environment (same as CI).
test:
	$(COMPOSE_TEST) run --rm --build tests
	$(COMPOSE_TEST) down -v

# Quick local run of the tests that need no external services.
test-local:
	go test ./...

# Performance scenarios at the scale of the documented numbers (a few minutes). Needs a MongoDB replica
# set in MONGO_TEST_URI; results are appended to perf-results/*.txt. See Development → Performance tests.
perf:
	@test -n "$$MONGO_TEST_URI" || { echo "set MONGO_TEST_URI to a MongoDB replica set (see Development → Testing)"; exit 1; }
	mkdir -p perf-results && rm -f perf-results/*.txt
	BACKD_LOAD_SCALE=10 BACKD_SOAK=30 BACKD_LOAD_REPORT=$(CURDIR)/perf-results/triggers.txt go test ./internal/triggers -run TestLoad -count=1 -timeout 900s
	BACKD_LOAD_SCALE=10 BACKD_SOAK=30 BACKD_LOAD_REPORT=$(CURDIR)/perf-results/realtime.txt go test ./internal/realtime -run TestLoad -count=1 -timeout 900s
	@echo; echo "--- triggers"; cat perf-results/triggers.txt; echo "--- realtime"; cat perf-results/realtime.txt

vet:
	go vet ./...

# golangci-lint (v2; install: https://golangci-lint.run/welcome/install/); config in .golangci.yml.
# CI runs the same through golangci-lint-action.
lint:
	golangci-lint run ./...

# JavaScript client: type-check and unit tests (needs Node).
js-test:
	cd clients/js && npm ci --silent && npm run typecheck && npm test
	node --test internal/executor/steps.test.js

# The JavaScript client packed and installed the way npm users get it, used from
# Node and TypeScript (needs Node; see scripts/check-js-package.sh).
js-package:
	cd clients/js && npm ci --silent
	scripts/check-js-package.sh

# JavaScript client against a real backd + MongoDB (docker-compose.js.yml).
js-integration:
	cd clients/js && npm ci --silent
	clients/js/test/integration/run.sh

# Dart and pub.dev (needs Dart; see scripts/check-dart-package.sh).
dart-package:
	cd clients/dart && dart pub get
	scripts/check-dart-package.sh

# Dart client against a real backd + MongoDB (docker-compose.dart.yml).
dart-integration:
	clients/dart/test/integration/run.sh

# @backd/functions-testing's own tests (needs Deno; the dockerized test
# image already has it).
functions-testing-test:
	$(COMPOSE_TEST) run --rm --build --entrypoint deno tests test clients/functions-testing/src/ clients/functions-testing/examples/ examples/config/workshop/main/_functions/ examples/config/workshop/notifications/_functions/
	$(COMPOSE_TEST) run --rm --entrypoint deno tests test --config examples/config/shelf/main/_functions/deno.json examples/config/shelf/main/_functions/
	$(COMPOSE_TEST) down -v

# The Shelf's generated types are current: the app's (clients/js/examples/shelf/backd) and each
# functions project's backd.d.ts. After a schema change: `make shelf-types-update`.
shelf-types:
	CONFIG_DIR=examples/config go run ./cmd/backd types --realm shelf --out clients/js/examples/shelf/backd --check
	CONFIG_DIR=examples/config go run ./cmd/backd types --realm shelf --functions --check

shelf-types-update:
	CONFIG_DIR=examples/config go run ./cmd/backd types --realm shelf --out clients/js/examples/shelf/backd
	CONFIG_DIR=examples/config go run ./cmd/backd types --realm shelf --functions

# The admin UI (ui/admin): build it into internal/adminui/dist, where the Go
# build embeds it (a Go build without it still works; /_ui/ answers 404).
ui:
	cd clients/js && npm ci && cd ../../ui/admin && npm ci && npm run build

# Type-check, lint and unit-test the admin UI.
ui-test:
	cd clients/js && npm ci && cd ../../ui/admin && npm ci && npm run typecheck && npm run lint && npm test

# The admin UI's Playwright tests against the local example stack, brought up
# for the run (needs Docker; ports 8443 and 8080 free).
ui-e2e:
	scripts/ui-e2e.sh

# Lint api/openapi.yaml (needs Node). The Go contract tests check the server against it.
lint-api:
	npx --yes @redocly/cli@2 lint --config api/redocly.yaml api/openapi.yaml

# The example realms' generated OpenAPI documents lint clean and build Dart, Kotlin and
# Swift clients with openapi-generator (needs docker).
openapi-clients:
	scripts/check-openapi-clients.sh

docs:
	hugo --source docs --minify

docs-serve:
	hugo server --source docs

# The local stack on one origin, https://localhost:8443: backd + MongoDB,
# the docs site with live reload and the example app, behind nginx.
# Needs only Docker. Ctrl-C stops it all.
example:
	scripts/example.sh

# The development stack for people building ON backd: released images, your own ./config
# (CONFIG_DIR), live reload, the API and the worker apart, Prometheus and Grafana. Nothing is
# built; `make dev-build` pulls the images and makes the certificates once. docker-compose.dev.yml.
dev-build:
	scripts/dev.sh build

dev:
	scripts/dev.sh up

# The same with the repository's example realms and apps (blog, expenses, shelf, workshop, mailbox).
dev-examples:
	EXAMPLES=1 scripts/dev.sh up

dev-down:
	scripts/dev.sh down

dev-reset:
	scripts/dev.sh reset

# Follow the logs; S=worker (or backd, executor, ...) for one service.
dev-logs:
	scripts/dev.sh logs

# The backd command line in the running dev stack's API container:
#   make dev-backd ARGS='bootstrap --realm app --email you@example.com'
dev-backd:
	scripts/dev.sh backd $(ARGS)

# The production reference deployment (deploy/production), end to end in Docker.
prod-test:
	deploy/production/test.sh

# F4's egress proxy and network placement (docker/egress-test), end to end
# in Docker: a function can reach only what it declares and backd's
# callback, even via raw TCP to an address an allowed name resolves to.
egress-test:
	docker/egress-test/test.sh

# Attacks the expenses example on the running local stack (make example):
# checks that the rules stop what they can, and shows the known holes.
hack-expenses:
	NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/expenses-without-functions/hack.js

# The same attacks against expenses-with-functions: holes 1-4 report
# closed (hole 5 stays open; nothing here changes it).
hack-expenses-functions:
	NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/expenses-with-functions/hack.js

# Walks the workshop tour (clients/js/examples/workshop) without a browser on
# the running local stack (make example) and checks what each step says: the
# server's tax, a signed webhook, the idempotent refund and its receipt, the
# async report, and what an operator can see and do.
workshop-tour:
	NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/workshop/tour.js

# Replays the shelf tutorial's claims (clients/js/examples/shelf) without a
# browser on the running tutorial stack (its compose file, BACKD_URL
# http://localhost:8080). On a fresh stack it bootstraps the operator through
# `docker compose exec backd` — run it where that project is up.
shelf-tour:
	node clients/js/examples/shelf/tour.js

# The whole tutorial, from a starter folder built out of this checkout with
# locally built images (what CI runs). Needs port 8080 free.
shelf-tour-ci:
	scripts/shelf-tour-ci.sh

# Checks the release configuration and builds every release artifact into
# dist/, without publishing (the release workflow runs on version tags).
release-check:
	docker run --rm -v "$(CURDIR)":/src:Z -w /src docker.io/goreleaser/goreleaser:v2.18.2 check
	docker run --rm -v "$(CURDIR)":/src:Z -w /src docker.io/goreleaser/goreleaser:v2.18.2 release --snapshot --clean --skip=publish

# Starts its own local stack and runs every attack script against it (what CI
# does); stops the stack afterwards. Stop `make example` first.
example-attacks:
	scripts/example-ci.sh
