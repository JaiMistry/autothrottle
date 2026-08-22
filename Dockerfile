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
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/JaiMistry/autothrottle/internal/version.Version=${VERSION}" \
      -o /out/autothrottle ./cmd/autothrottle
# Pre-create /data owned by distroless's nonroot UID (65532) here, since
# the final stage has no shell to chown with. A named volume mounted at
# this path inherits this ownership on first creation.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/autothrottle /autothrottle
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/autothrottle"]
