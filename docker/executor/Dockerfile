# The functions executor image (backd-executor): the official Deno image,
# pinned to the version backd runs functions with (registry.DenoVersion),
# plus the backd binary, running `backd executor`. Build context: the
# repository root.
#
# Run it non-root with a read-only root filesystem and a tmpfs for its
# working directory (EXECUTOR_DIR), on a network that reaches only backd's
# internal listener (see the functions docs).
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/backd ./cmd/backd

FROM docker.io/denoland/deno:alpine-2.9.7
COPY --from=build /out/backd /usr/local/bin/backd
ENV EXECUTOR_DIR=/tmp/backd-executor
USER deno
EXPOSE 9100
ENTRYPOINT ["/usr/local/bin/backd", "executor"]
