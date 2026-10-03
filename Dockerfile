FROM node:22-slim AS tailwind
# Build-time only -- Node never ships in the final image (see the last
# stage below). daisyUI is a Tailwind plugin that needs real Node
# module resolution; the standalone Tailwind CLI binary can't resolve
# third-party npm plugins on its own.
WORKDIR /src
COPY package.json package-lock.json ./
# npm ci, not npm install: the lockfile is committed, so the CSS build
# is reproducible rather than resolving fresh versions per build.
RUN npm ci
COPY internal/web/templates ./internal/web/templates
COPY internal/web/static ./internal/web/static
# Built in place rather than from a copied-out entry file: input.css
# @imports ./tokens/*.css, which only resolve from their real location.
RUN npx @tailwindcss/cli -i ./internal/web/static/css/input.css -o /out/tailwind.css --minify
# ApexCharts is vendored rather than loaded from a CDN — this is a
# self-hosted LAN app that may have no outbound network, and pinning it
# in package.json keeps Renovate able to update it.
RUN cp node_modules/apexcharts/dist/apexcharts.min.js /out/apexcharts.min.js

FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=tailwind /out/tailwind.css internal/web/static/css/tailwind.css
COPY --from=tailwind /out/apexcharts.min.js internal/web/static/js/apexcharts.min.js
ARG VERSION=dev
# The build context carries no .git, so the revision and build date the
# toolchain would normally embed have to be passed in. Both are optional:
# without them About falls back to what the binary can tell about itself.
ARG COMMIT=""
ARG BUILD_DATE=""
# Compilation is the memory peak of this image, and modernc.org/sqlite
# is a very large package to build. Unbounded, the toolchain starts one
# compile per core and the peak scales with the build machine's core
# count rather than its RAM — which is how a build on a busy host
# becomes an out-of-memory kill. Two at a time keeps the peak flat and
# costs a few seconds.
ENV GOFLAGS=-p=2 GOMAXPROCS=2
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w \
        -X github.com/JaiMistry/autothrottle/internal/version.Version=${VERSION} \
        -X github.com/JaiMistry/autothrottle/internal/version.Commit=${COMMIT} \
        -X github.com/JaiMistry/autothrottle/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/autothrottle ./cmd/autothrottle
RUN mkdir -p /out/data

# Starts as root on purpose: the binary hands /data to PUID:PGID and
# then drops to that user before it opens the database or a socket.
FROM gcr.io/distroless/static-debian12
# Timestamps in the UI are rendered in this zone. Override it at run
# time with -e TZ=... ; the zone database is compiled into the binary,
# so any IANA name works without adding tzdata to the image.
ENV TZ=America/Los_Angeles
COPY --from=build /out/autothrottle /autothrottle
COPY --from=build /out/data /data
EXPOSE 7676
ENTRYPOINT ["/autothrottle"]
