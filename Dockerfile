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
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2-alpine AS build
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/backd /backd
EXPOSE 8080
ENTRYPOINT ["/backd"]
# `backd` without a command prints the help; the image serves unless told otherwise.
CMD ["serve"]
