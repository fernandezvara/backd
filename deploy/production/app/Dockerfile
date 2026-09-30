# The deployable artifact: backd plus this deployment's CONFIG_DIR, built
# once (in CI, from one tag of the config repository) and run unchanged by
# every instance, in staging and in production (docs: Deploying config).
#
# In your own config repository, start from the released image instead:
#
#   FROM ghcr.io/fernandezvara/backd:<version>
#   COPY config /config
#   ENV CONFIG_DIR=/config
#   RUN ["/backd", "config", "check"]
#
# Here backd is built from this checkout, so the reference tests this code.
# Build context: the repository root.
FROM docker.io/library/golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backd ./cmd/backd

# Bundles functions/netprobe's Deno source into the config (see its
# realm.yaml): so it ships pre-built, and byte-identical to ops/Dockerfile's
# copy, since PROVISION_MODE=verify compares their config fingerprints.
FROM docker.io/denoland/deno:alpine-2.9.7 AS functions
COPY --from=build /out/backd /usr/local/bin/backd
COPY examples/config/blog /config/blog
COPY deploy/production/functions/netprobe /config/netprobe
ENV CONFIG_DIR=/config
RUN /usr/local/bin/backd functions build --check

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/backd /backd
# The config: realms, schemas, rules and built function bundles.
COPY --from=functions /config /config
ENV CONFIG_DIR=/config
# An invalid config (or stale function bundles) fails the build, not a deploy.
RUN ["/backd", "config", "check"]
EXPOSE 8080 8081
ENTRYPOINT ["/backd"]
