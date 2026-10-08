# The artifact this project deploys: a released backd plus this config,
# checked at build time so an invalid config never becomes an image. Pin
# BACKD_VERSION to the backd release you run everywhere (docs: Operations
# → Deploying config).
ARG BACKD_VERSION=0.8.1
FROM ghcr.io/fernandezvara/backd:v${BACKD_VERSION}
COPY config /config
ENV CONFIG_DIR=/config
RUN ["/backd", "config", "check"]
