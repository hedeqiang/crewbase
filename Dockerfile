# syntax=docker/dockerfile:1.7
#
# Crewbase headless server image (`crewd --profile server`).
#
#   docker build -t crewbase .                                  # distroless, no browser
#   docker build --build-arg RUNTIME=chromium -t crewbase:chromium .   # alpine + Chromium
#
# Build args:
#   RUNTIME          distroless (default) | chromium
#   GO_VERSION       Go toolchain image tag (go.mod requires 1.27)
#   NODE_VERSION     Node image tag for the web UI build
#   GOPROXY          e.g. https://goproxy.cn,direct in mainland China
#   NPM_REGISTRY     e.g. https://registry.npmmirror.com
#   ALPINE_MIRROR    e.g. mirrors.aliyun.com (chromium runtime only)
#
# The image exposes no ports. See docs/deploy.md.

ARG GO_VERSION=1.27
ARG NODE_VERSION=22
ARG RUNTIME=distroless

# ---------------------------------------------------------------- web UI
FROM node:${NODE_VERSION}-alpine AS web
ARG NPM_REGISTRY=
WORKDIR /src/apps/web
RUN corepack enable
COPY apps/web/package.json apps/web/pnpm-lock.yaml ./
RUN if [ -n "$NPM_REGISTRY" ]; then pnpm config set registry "$NPM_REGISTRY"; fi \
 && (pnpm install --frozen-lockfile || pnpm install)
COPY apps/web/ ./
RUN pnpm build

# ---------------------------------------------------------------- Go binaries
FROM golang:${GO_VERSION}-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
# The embedded UI comes from the web stage, never from the build context.
RUN rm -rf apps/web/dist && mkdir -p apps/web/dist && touch apps/web/dist/.keep
COPY --from=web /src/apps/web/dist/ apps/web/dist/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/crewd ./apps/crewd \
 && go build -ldflags="-s -w" -o /out/crew ./apps/cli \
 && mkdir -p /out/data/home && chown -R 65532:65532 /out/data && chmod 700 /out/data

# ---------------------------------------------------------------- runtimes
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-distroless

FROM alpine:3.22 AS runtime-chromium
ARG ALPINE_MIRROR=
RUN if [ -n "$ALPINE_MIRROR" ]; then sed -i "s#dl-cdn.alpinelinux.org#${ALPINE_MIRROR}#g" /etc/apk/repositories; fi \
 && apk add --no-cache chromium nss freetype harfbuzz ttf-freefont font-noto-cjk ca-certificates tzdata \
 && addgroup -g 65532 -S nonroot && adduser -u 65532 -S -G nonroot -h /data/home -H nonroot
ENV CREWBASE_BROWSER=/usr/bin/chromium

# ---------------------------------------------------------------- final
FROM runtime-${RUNTIME}
COPY --from=build /out/crewd /out/crew /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/data /data
ENV CREWBASE_HOME=/data HOME=/data/home TMPDIR=/tmp
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
# No EXPOSE: the gateway binds 127.0.0.1 inside the container; reach it with
# `docker exec`, an SSH tunnel to the host, Tailscale or the relay.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/crew", "doctor", "--health"]
ENTRYPOINT ["/usr/local/bin/crewd"]
CMD ["--profile", "server"]
