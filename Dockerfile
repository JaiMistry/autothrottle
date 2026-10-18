# Both build stages run on the builder's own platform, whatever the image
# is for: the CSS is the same on every architecture, and Go cross-compiles
# (CGO is off), so an arm64 image never runs its compiler under emulation.
FROM --platform=$BUILDPLATFORM node:22-slim AS tailwind
# Build-time only: Node never ships in the final image.
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY internal/web/templates ./internal/web/templates
COPY internal/web/static ./internal/web/static
# Built in place rather than from a copied-out entry file: input.css
# @imports ./tokens/*.css, which only resolve from their real location.
RUN npx @tailwindcss/cli -i ./internal/web/static/css/input.css -o /out/tailwind.css --minify

FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=tailwind /out/tailwind.css internal/web/static/css/tailwind.css
# modernc.org/sqlite is huge to compile; capping parallelism keeps the
# build's memory peak flat instead of scaling with the core count.
ENV GOFLAGS=-p=2 GOMAXPROCS=2

FROM source AS build
ARG VERSION=dev
# The build context carries no .git, so the revision and build date the
# toolchain would normally embed have to be passed in. Both are optional:
# without them About falls back to what the binary can tell about itself.
ARG COMMIT=""
ARG BUILD_DATE=""
# Set by BuildKit to the platform being built for.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w \
        -X github.com/JaiMistry/autothrottle/internal/version.Version=${VERSION} \
        -X github.com/JaiMistry/autothrottle/internal/version.Commit=${COMMIT} \
        -X github.com/JaiMistry/autothrottle/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/autothrottle ./cmd/autothrottle
# A binary for the wrong architecture would still build, and the smoke
# test only starts amd64: check what the toolchain recorded instead.
RUN go version -m /out/autothrottle | grep -qw "GOARCH=${TARGETARCH:-$(go env GOARCH)}"
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
# Is the app serving? Not whether its media servers and download clients
# are: those show on the Connections page. Every 2s while it starts, so
# it reads healthy within seconds, then every 30s.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=2s --retries=3 \
  CMD ["/autothrottle", "healthcheck"]
ENTRYPOINT ["/autothrottle"]
