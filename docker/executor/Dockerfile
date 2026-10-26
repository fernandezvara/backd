# The functions executor image (backd-executor): the official Deno image,
# pinned to the version backd runs functions with (registry.DenoVersion),
# plus the backd binary, running `backd executor`. Build context: the repository root.
#
# Run it non-root with a read-only root filesystem and a tmpfs for its
# working directory (EXECUTOR_DIR), on a network that reaches only backd's
# internal listener (see the functions docs).
#
# The binary has the admin UI embedded, as in the backd image, because the development stack
# (docker-compose.dev.yml) runs the API and the worker from this image: it has Deno, which dev
# mode needs to bundle functions when they change. A production executor never serves the UI.
FROM --platform=$BUILDPLATFORM docker.io/library/node:22-alpine AS ui
WORKDIR /src
COPY clients/js clients/js
COPY ui/admin ui/admin
RUN mkdir -p internal/adminui/dist && (cd clients/js && npm ci --ignore-scripts --no-audit --no-fund) && cd ui/admin && npm ci --no-audit --no-fund && npm run build

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/adminui/dist ./internal/adminui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/backd ./cmd/backd

FROM docker.io/denoland/deno:alpine-2.9.7
# The base image lags behind Alpine's security fixes (the release scan failed
# on OpenSSL once), so pick them up at build time.
USER root
RUN apk upgrade --no-cache
COPY --from=build /out/backd /usr/local/bin/backd
# /backd is a link to it, for `docker compose exec backd /backd <command>`; the same in every image.
RUN ln -s /usr/local/bin/backd /backd
ENV EXECUTOR_DIR=/tmp/backd-executor
USER deno
EXPOSE 9100
ENTRYPOINT ["/usr/local/bin/backd", "executor"]
