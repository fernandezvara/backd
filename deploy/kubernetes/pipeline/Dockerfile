# The configuration image: the released backd plus this installation's realm tree and the built
# functions (design/kubernetes.md §4). Build it from the root of your configuration repository,
# where config/ is CONFIG_DIR:
#
#   docker build -f <backd checkout>/deploy/kubernetes/pipeline/Dockerfile \
#     --build-arg BACKD_VERSION=v0.9.6 -t <registry>/<name>:<release> .
#
# An invalid configuration, or a function that does not bundle, fails the build: it never becomes
# an image that can be deployed.
ARG BACKD_VERSION

# The released executor image has Deno (the version this backd release runs functions with) and
# the backd binary, which is all that bundling functions needs.
FROM ghcr.io/fernandezvara/backd-executor:${BACKD_VERSION} AS build
USER root
ENV CONFIG_DIR=/config HOME=/tmp DENO_DIR=/tmp/deno
COPY config /config
RUN /usr/local/bin/backd functions build
RUN /usr/local/bin/backd config check

# The deployable image has no Deno and no shell: it only reads the configuration.
FROM ghcr.io/fernandezvara/backd:${BACKD_VERSION}
COPY --from=build /config /config
ENV CONFIG_DIR=/config
# Checked again in the image that will run: the fingerprint printed here is the one `backd provision`
# records and the pods verify.
RUN ["/backd", "config", "check"]
