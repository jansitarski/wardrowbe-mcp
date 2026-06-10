# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-06-10

First public release.

### Added
- MCP server exposing the Wardrowbe wardrobe API as 31 tools over Streamable HTTP
  and stdio: browsing/analytics, wear/wash/archive lifecycle, outfit suggestion and
  feedback, image-view tools, tag/description write-back, and item/outfit creation.
- Hardened public HTTP surface: static constant-time bearer gate (RFC 9728
  `WWW-Authenticate` on 401), inbound body-size cap, concurrency limiter with
  `503 Retry-After`, panic recovery, and `X-Content-Type-Options`/`Cache-Control`
  security headers.
- SSRF-guarded external image fetch for `create_item_from_url`: http(s)-only,
  per-hop IP re-validation (defeats DNS rebinding), redirect-scheme re-validation,
  and rejection of private/loopback/link-local/CGNAT/multicast/NAT64 targets.
- OIDC auth mode with issuer host-pinning, https-only enforcement, and cached
  discovery; Helm support for OIDC secret material via a Kubernetes Secret (env),
  never pod args.
- Multi-arch (amd64/arm64) distroless non-root container image and an OCI Helm
  chart, both published per release.
- CI: gofmt/vet/build, race + coverage unit tests, end-to-end integration test of
  every tool, helm lint/template, plus `govulncheck` and `gosec` security scans.
  GitHub Actions pinned to commit SHAs with least-privilege, per-job permissions.

[Unreleased]: https://github.com/jansitarski/wardrowbe-mcp/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/jansitarski/wardrowbe-mcp/releases/tag/v1.0.0
