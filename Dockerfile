# The build stage runs on the build machine's platform and cross-compiles
# for the target one, so multi-arch images build without emulation.
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/backd /backd
EXPOSE 8080
ENTRYPOINT ["/backd"]
