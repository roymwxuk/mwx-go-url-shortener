# syntax=docker/dockerfile:1

# ---- build ----
# Runs on the builder's native platform and cross-compiles, so multi-arch builds
# (e.g. linux/amd64 + linux/arm64 for Graviton) don't need QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime ----
# Static binary on distroless: no shell or package manager, runs as non-root,
# and ships CA certificates for TLS to RDS. SQL migrations are embedded in the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server

# All configuration (DATABASE_URL, BASE_URL, rate limits, ...) comes from the
# environment at runtime; nothing is baked into the image.
ENV ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
