# The admin UI (ui/admin) is built first and embedded in the binary.
# The linked backd-js (clients/js) builds its typings when installed, so its own
# dependencies come first.
FROM --platform=$BUILDPLATFORM docker.io/library/node:22-alpine AS ui
WORKDIR /src
COPY clients/js clients/js
COPY ui/admin ui/admin
RUN mkdir -p internal/adminui/dist && (cd clients/js && npm ci --ignore-scripts --no-audit --no-fund) && cd ui/admin && npm ci --no-audit --no-fund && npm run build

# The build stage runs on the build machine's platform and cross-compiles
# for the target one, so multi-arch images build without emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/adminui/dist ./internal/adminui/dist
# Both binaries live in /usr/local/bin in every backd image: backd-server (the server roles and
# the tools on MongoDB) and backd (the CLI, for `docker compose exec backd backd whoami`).
# /backd and /backd-server are links to them. The distroless image has no shell, so the tree is
# laid out here and copied whole (a copied tree keeps the links).
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/fernandezvara/backd/internal/buildinfo.Version=${VERSION}" -o /out/backd-server ./cmd/backd-server \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/fernandezvara/backd/internal/buildinfo.Version=${VERSION}" -o /out/backd ./cmd/backd \
    && mkdir -p /rootfs/usr/local/bin && cp /out/backd /out/backd-server /rootfs/usr/local/bin/ \
    && ln -s /usr/local/bin/backd /rootfs/backd && ln -s /usr/local/bin/backd-server /rootfs/backd-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /rootfs/ /
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/backd-server"]
# The image serves unless told otherwise (`command: worker`, `egress`, `provision`…).
CMD ["serve"]
