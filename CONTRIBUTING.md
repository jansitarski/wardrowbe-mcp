# Contributing

Thanks for your interest in `wardrowbe-mcp`. This is a small Go project; the bar
is simple.

## Development

Go 1.25+ is required. Common tasks are in the `Makefile`:

```bash
make build   # build the binary
make test    # go test -race ./...
make vet     # go vet ./...
make fmt     # gofmt -w .
make lint    # vet + gofmt -l (lists unformatted files)
```

CI runs `gofmt -l`, `go vet`, `go build`, and `go test -race` on every push and
pull request. Please make sure all four pass locally before opening a PR.

## Guidelines

- **Format with `gofmt`** — CI fails on unformatted files.
- **Add tests** for new behavior; cover the error paths, not just the happy path.
- **Keep secrets out of the repo.** Configuration comes from flags and
  environment variables only (see `README.md`).
- **Conventional commits** (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`,
  `chore:`) keep the history readable.
- New MCP tools go in `internal/mcpserver/tools_*.go` and are registered from
  `server.go`; mark read-only tools with `WithReadOnlyHintAnnotation(true)` and
  destructive ones with `WithDestructiveHintAnnotation(true)`.

## Reporting security issues

Please do not file public issues for vulnerabilities — see
[`docs/SECURITY.md`](docs/SECURITY.md).
