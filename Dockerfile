# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/wardrowbe-mcp ./cmd/wardrowbe-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wardrowbe-mcp /wardrowbe-mcp
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/wardrowbe-mcp"]
