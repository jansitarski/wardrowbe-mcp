# syntax=docker/dockerfile:1

# Build on the native builder arch and cross-compile to the target arch via
# Go's GOOS/GOARCH (set from buildx's TARGET* args). CGO is disabled, so the
# compile step needs no QEMU — only the final image layer is per-platform.
FROM --platform=$BUILDPLATFORM golang:1.25.11 AS build
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wardrowbe-mcp /wardrowbe-mcp
COPY --from=build /src/LICENSE /LICENSE
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/wardrowbe-mcp"]
