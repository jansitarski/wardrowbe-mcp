# syntax=docker/dockerfile:1

# Build on the native builder arch and cross-compile to the target arch via
# Go's GOOS/GOARCH (set from buildx's TARGET* args). CGO is disabled, so the
# compile step needs no QEMU — only the final image layer is per-platform.
# Base images are pinned by digest (the tag is kept for readability); Dependabot
# bumps the digest when the tag moves.
FROM --platform=$BUILDPLATFORM golang:1.25.11@sha256:379065f16fe8cce7949001ba9cffc827cd4b93d69495dec405befd1c13e19bb3 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X 'github.com/jansitarski/wardrowbe-mcp/internal/mcpserver.serverVersion=${VERSION}'" \
    -o /out/wardrowbe-mcp ./cmd/wardrowbe-mcp

FROM gcr.io/distroless/static-debian12:nonroot@sha256:b7bb25d9f7c31d2bdd1982feb4dafcaf137703c7075dbe2febb41c24212b946f
COPY --from=build /out/wardrowbe-mcp /wardrowbe-mcp
COPY --from=build /src/LICENSE /LICENSE
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/wardrowbe-mcp"]
