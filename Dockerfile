# syntax=docker/dockerfile:1.7
# One image recipe for every binary: docker build --build-arg SERVICE=gateway .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG SERVICE=gateway
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE} && mkdir -p /out/certs

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
# Mount point for the mutual-TLS volume, owned by the runtime user so the
# compose certs job can write to it.
COPY --from=build --chown=65532:65532 /out/certs /certs
USER nonroot:nonroot
ENTRYPOINT ["/app"]
