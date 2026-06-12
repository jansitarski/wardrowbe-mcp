BINARY := wardrowbe-mcp
PKG := ./cmd/wardrowbe-mcp
IMAGE ?= ghcr.io/jansitarski/wardrowbe-mcp
VERSION ?= dev
LDFLAGS := -s -w -X 'github.com/jansitarski/wardrowbe-mcp/internal/mcpserver.serverVersion=$(VERSION)'

.PHONY: build test vet fmt lint run docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(PKG)

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

run: build
ifndef WARDROWBE_URL
	$(error WARDROWBE_URL is not set (e.g. make run WARDROWBE_URL=http://127.0.0.1:8000 MCP_API_KEY=dev-key))
endif
ifndef MCP_API_KEY
	$(error MCP_API_KEY is not set (e.g. make run WARDROWBE_URL=http://127.0.0.1:8000 MCP_API_KEY=dev-key))
endif
	@# The key goes via the environment, not argv, so it never shows up in `ps`.
	MCP_API_KEY='$(MCP_API_KEY)' ./$(BINARY) --transport http --port 8080 \
		--wardrowbe-url $(WARDROWBE_URL)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

clean:
	rm -f $(BINARY)
