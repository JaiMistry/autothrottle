FROM node:22-slim AS tailwind
# Build-time only -- Node never ships in the final image (see the last
# stage below). daisyUI is a Tailwind plugin that needs real Node
# module resolution; the standalone Tailwind CLI binary can't resolve
# third-party npm plugins on its own.
WORKDIR /src
COPY package.json ./
RUN npm install
COPY internal/web/static/css/input.css ./input.css
COPY internal/web/templates ./internal/web/templates
COPY internal/web/static ./internal/web/static
RUN npx @tailwindcss/cli -i ./input.css -o /out/tailwind.css --minify

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=tailwind /out/tailwind.css internal/web/static/css/tailwind.css
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/autothrottle ./cmd/autothrottle
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
