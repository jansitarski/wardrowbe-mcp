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

### Integration test

`internal/mcpserver/integration_test.go` (build tag `integration`) drives **every
registered tool** through the real MCP protocol — an in-process client against a
faithful in-memory backend — and asserts each happy path returns a non-error
result and that the SSRF/validation guards reject bad input. It is hermetic (no
subprocess, no outbound network) and CI runs it as a separate step:

```bash
go test -tags integration -race ./internal/mcpserver/
```

When you add or rename a tool, update the cases in that file and bump
`expectedToolCount`.

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

## Releases

Releases are cut by pushing a `vX.Y.Z` git tag. The
[`release` workflow](.github/workflows/release.yml) then builds a multi-arch
(`amd64` + `arm64`) image, pushes it to `ghcr.io/jansitarski/wardrowbe-mcp`
tagged `X.Y.Z`, `X.Y`, and `latest`, and cuts a GitHub Release with
auto-generated notes. The tag version is baked into the binary via `-ldflags`,
so `wardrowbe-mcp --help`/the MCP handshake report the real version.

The same workflow packages the [Helm chart](charts/wardrowbe-mcp/) with the tag
as both chart and app version and pushes it to
`oci://ghcr.io/jansitarski/charts/wardrowbe-mcp`, so the chart version always
maps to an image that exists. CI runs `helm lint` + `helm template` on every PR;
bump `charts/wardrowbe-mcp/Chart.yaml` when the chart's templates change
independently of the app.

```bash
# 1. bump the default in internal/mcpserver/server.go only if you want a sane
#    `dev` fallback; the published version comes from the tag, not the source.
# 2. tag the release commit on master and push the tag:
git tag v1.0.0
git push origin v1.0.0
# 3. update your deployment (e.g. the Helm chart/image version) to the new tag.
```

## Reporting security issues

Please do not file public issues for vulnerabilities — see
[`docs/SECURITY.md`](docs/SECURITY.md).
