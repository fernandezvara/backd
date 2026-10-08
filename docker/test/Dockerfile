# The dockerized test environment (docker-compose.test.yml): Go plus the
# Deno version backd bundles functions with, so the build tests run too.
FROM docker.io/library/golang:1.27.2-alpine AS go

FROM docker.io/denoland/deno:alpine-2.9.7
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH GOPATH=/go GOTOOLCHAIN=local
WORKDIR /src
