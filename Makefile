BINARY := wardrowbe-mcp
PKG := ./cmd/wardrowbe-mcp
IMAGE ?= ghcr.io/jansitarski/wardrowbe-mcp-go
VERSION ?= dev

.PHONY: build test vet fmt lint run docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet
	gofmt -l .

run: build
	./$(BINARY) --transport http --port 8080 \
		--wardrowbe-url $(WARDROWBE_URL) --api-key $(MCP_API_KEY)

docker:
	docker build -t $(IMAGE):$(VERSION) .

clean:
	rm -f $(BINARY)
