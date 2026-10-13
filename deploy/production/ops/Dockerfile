# Operations image for the production reference: the MongoDB tools
# (mongosh, mongodump, mongorestore), age for encrypting backups, and the
# backd binary with the same config as the app image (app/Dockerfile), from
# the same checkout: provisioning records its fingerprint, and backd in
# verify mode refuses to start if its own differs. Used by the provision,
# backup and restore jobs; build context is the repository root.
FROM docker.io/library/golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backd ./cmd/backd

# Bundles functions/netprobe's Deno source, byte-identical to app/Dockerfile's
# copy (see its comment): the provision job's fingerprint must match what
# backd itself loads.
FROM docker.io/denoland/deno:alpine-2.9.7 AS functions
COPY --from=build /out/backd /usr/local/bin/backd
COPY examples/config/blog /config/blog
COPY deploy/production/functions/netprobe /config/netprobe
ENV CONFIG_DIR=/config
RUN /usr/local/bin/backd functions build --check

FROM docker.io/library/mongo:8.2
RUN apt-get update && apt-get install -y --no-install-recommends age && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/backd /usr/local/bin/backd
# The MinIO client, for storage-setup.sh (MinIO publishes no image of its own now;
# Chainguard's build has just the binary).
COPY --from=cgr.dev/chainguard/minio-client:latest /usr/bin/mc /usr/local/bin/mc
COPY deploy/production/ops/*.sh deploy/production/ops/*.js deploy/production/ops/*.json /ops/
COPY --from=functions /config /config
USER mongodb
ENTRYPOINT []
