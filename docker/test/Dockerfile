# The dockerized test environment (docker-compose.test.yml): Go plus the
# Deno version backd bundles functions with, so the build tests run too.
FROM docker.io/library/golang:1.27.4-alpine AS go

FROM docker.io/denoland/deno:alpine-2.9.8
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH GOPATH=/go GOTOOLCHAIN=local
WORKDIR /src
# `deno bundle` downloads esbuild into DENO_DIR the first time it runs. The test packages bundle
# functions at the same time, and two of them installing it together fail ("failed to copy esbuild
# binary"), so it is installed once here.
RUN mkdir /tmp/warm && echo 'export default 1;' > /tmp/warm/a.ts && deno bundle /tmp/warm/a.ts -o /tmp/warm/out.js && rm -rf /tmp/warm
