FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/autothrottle ./cmd/autothrottle

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/autothrottle /autothrottle
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/autothrottle"]
