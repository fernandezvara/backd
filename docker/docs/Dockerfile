# Hugo server for the documentation site, with live reload. Hugo modules
# (the hugodoks theme) need Go and Git, so this starts from the Go image.
FROM docker.io/library/golang:1.27.2-bookworm

ARG HUGO_VERSION=0.154.5
RUN arch="$(dpkg --print-architecture)" \
 && curl -fsSL "https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_extended_${HUGO_VERSION}_linux-${arch}.tar.gz" \
    | tar -xz -C /usr/local/bin hugo \
 && hugo version \
 # The repository is mounted from the host, owned by another user.
 && git config --system --add safe.directory '*'

# Caches live outside the (read-only) source tree.
ENV HUGO_CACHEDIR=/cache/hugo \
    HUGO_RESOURCEDIR=/cache/resources \
    GOMODCACHE=/cache/gomod \
    GOFLAGS=-modcacherw

WORKDIR /src/docs
EXPOSE 1313
# The browser reaches Hugo through nginx on https://localhost:8443, so pages and
# the live-reload script must point there. Hugo writes static files even
# when serving from memory, and the source is read-only: --destination.
CMD ["hugo", "server", "--bind", "0.0.0.0", "--port", "1313", \
     "--baseURL", "https://localhost:8443/", "--appendPort=false", \
     "--liveReloadPort", "8443", "--noBuildLock", "--destination", "/tmp/public"]
