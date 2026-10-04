COMPOSE_TEST = docker compose -f docker-compose.test.yml

.PHONY: build test test-local vet lint-api js-test js-package js-integration functions-testing-test docs docs-serve example prod-test egress-test hack-expenses hack-expenses-functions workshop-tour shelf-tour example-attacks release-check

build:
	go build -o bin/backd ./cmd/backd

# Full test suite in the dockerized environment (same as CI).
test:
	$(COMPOSE_TEST) run --rm --build tests
	$(COMPOSE_TEST) down -v

# Quick local run of the tests that need no external services.
test-local:
	go test ./...

vet:
	go vet ./...

# JavaScript client: type-check and unit tests (needs Node).
js-test:
	cd clients/js && npm ci --silent && npm run typecheck && npm test

# The JavaScript client packed and installed the way npm users get it, used from
# Node and TypeScript (needs Node; see scripts/check-js-package.sh).
js-package:
	cd clients/js && npm ci --silent
	scripts/check-js-package.sh

# JavaScript client against a real backd + MongoDB (docker-compose.js.yml).
js-integration:
	cd clients/js && npm ci --silent
	clients/js/test/integration/run.sh

# @backd/functions-testing's own tests (needs Deno; the dockerized test
# image already has it).
functions-testing-test:
	$(COMPOSE_TEST) run --rm --build --entrypoint deno tests test clients/functions-testing/src/ examples/config/workshop/main/_functions/ examples/config/workshop/notifications/_functions/ examples/config/shelf/main/_functions/
	$(COMPOSE_TEST) down -v

# Lint api/openapi.yaml (needs Node). The Go contract tests check the server against it.
lint-api:
	npx --yes @redocly/cli@2 lint --config api/redocly.yaml api/openapi.yaml

docs:
	hugo --source docs --minify

docs-serve:
	hugo server --source docs

# The local stack on one origin, https://localhost:8443: backd + MongoDB,
# the docs site with live reload and the example app, behind nginx.
# Needs only Docker. Ctrl-C stops it all.
example:
	scripts/example.sh

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

# Checks the release configuration and builds every release artifact into
# dist/, without publishing (the release workflow runs on version tags).
release-check:
	docker run --rm -v "$(CURDIR)":/src:Z -w /src docker.io/goreleaser/goreleaser:v2.18.2 check
	docker run --rm -v "$(CURDIR)":/src:Z -w /src docker.io/goreleaser/goreleaser:v2.18.2 release --snapshot --clean --skip=publish

# Starts its own local stack and runs every attack script against it (what CI
# does); stops the stack afterwards. Stop `make example` first.
example-attacks:
	scripts/example-ci.sh
