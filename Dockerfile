# syntax=docker/dockerfile:1

# Build on the native builder arch and cross-compile to the target arch via
# Go's GOOS/GOARCH (set from buildx's TARGET* args). CGO is disabled, so the
# compile step needs no QEMU — only the final image layer is per-platform.
# Base images are pinned by digest (the tag is kept for readability); Dependabot
# bumps the digest when the tag moves.
FROM --platform=$BUILDPLATFORM golang:1.26.4@sha256:87a41d2539e5671777734e91f467499ed5eafb1fb1f77221dff2744db7a51775 AS build
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

FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639
COPY --from=build /out/wardrowbe-mcp /wardrowbe-mcp
COPY --from=build /src/LICENSE /LICENSE
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/wardrowbe-mcp"]
