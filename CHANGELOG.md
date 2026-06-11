# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-06-12

First public release. Changes below are relative to v0.3.0 and include a full
pre-release security/correctness review pass.

### Added
- MCP server exposing the Wardrowbe wardrobe API as 32 tools over Streamable
  HTTP and stdio: browsing/analytics, wear/wash/archive lifecycle, outfit
  suggestion and feedback, image-view tools, tag/description write-back, and
  item/outfit creation.
- Hardened public HTTP surface: static constant-time bearer gate (RFC 9728
  `WWW-Authenticate` on 401), inbound body-size cap, concurrency limiter with
  `503 Retry-After`, panic recovery, and `X-Content-Type-Options`/`Cache-Control`
  security headers.
- SSRF-guarded external image fetch for `wardrowbe_create_item_from_url`:
  http(s)-only, per-hop IP re-validation (defeats DNS rebinding),
  redirect-scheme re-validation, and rejection of
  private/loopback/link-local/CGNAT/multicast/NAT64 targets.
- OIDC auth mode (https-only issuer, cached discovery, optional
  `--oidc-token-endpoint` override, refresh-token rotation support); Helm
  support for OIDC secret material via a Kubernetes Secret (env), never pod
  args. RFC 6749 error bodies (`invalid_grant`, …) are surfaced instead of a
  bare status code.
- `wardrowbe_list_notification_settings` tool — surfaces the `setting_id` that
  `wardrowbe_test_notification` requires.
- MCP `destructiveHint`/`idempotentHint` annotations on every write tool, so
  reversible actions (e.g. `wardrowbe_archive_item`) are distinguishable from
  the permanent `wardrowbe_delete_outfit`.
- Multi-arch (amd64/arm64) distroless non-root container image (bases pinned
  by digest) and an OCI Helm chart, both published per release.
- Prebuilt static linux binaries (amd64, arm64, armv7) with `SHA256SUMS.txt`
  and signed build-provenance attestations attached to every GitHub Release;
  a `--version` flag reports the build version.
- CI: gofmt/vet/build, race + coverage unit tests, end-to-end integration test
  of every tool, helm lint/template, a multi-arch Docker smoke build,
  checksum-verified `gosec` and `govulncheck` scans, and a test gate in the
  release pipeline. GitHub Actions pinned to commit SHAs with least-privilege,
  per-job permissions.
- Helm: a `checksum/api-key` pod annotation rolls the Deployment when the
  chart-managed API key is rotated; `automountServiceAccountToken: false`;
  `terminationGracePeriodSeconds: 30`; template-time validation of transport,
  API-key and OIDC prerequisites.

### Changed
- **Breaking:** all tool names now carry a `wardrowbe_` service prefix
  (`list_items` → `wardrowbe_list_items`, …) so aggregating MCP hosts can
  disambiguate them. Saved client configs and permission allowlists need
  updating.
- `wardrowbe_list_items` now applies a default `page_size` of 25 when none is
  given (was: returned the entire wardrobe), and documents its pagination.
- `--wardrowbe-url` no longer silently defaults to `http://127.0.0.1:8000`; it
  is required and startup fails with a clear message when missing.
- OIDC: the discovered `token_endpoint` is no longer pinned to the issuer host
  (https is still required) — fixes Google and AWS Cognito, whose token
  endpoints live on a different domain. A new `--oidc-token-endpoint`
  (`MCP_OIDC_TOKEN_ENDPOINT`) override skips discovery for operators who want
  an explicit pin.
- `--help` now exits 0, parse errors exit 2 without a duplicated `fatal:` line,
  and `--version` is a real flag instead of an argv scan.
- A drain-timeout on SIGTERM now closes remaining connections and exits 0
  (was: exit 1 with `fatal: context deadline exceeded` on routine rollouts); a
  second Ctrl-C during the drain force-exits.
- Token refresh against the backend is single-flight: concurrent requests no
  longer block on a mutex held across the network call (they honor their own
  context cancellation), and a 401 storm no longer triggers a re-sync herd.
- Backend write tools validate strictly: present-but-uncoercible arguments
  (`favorite: "yes"`, `rating: "high"`) are rejected instead of silently
  writing defaults; out-of-range ratings are rejected instead of clamped;
  empty/whitespace IDs are rejected (previously `get_item("")` returned the
  whole collection); `archive_item` enforces the backend's 50-char reason
  limit with a clear error.
- `wardrowbe_accept/reject/skip_latest_outfit` take an optional `outfit_id`
  (closing the view-act race on "latest") and report the resolved outfit id.
- The Helm chart refuses `config.transport=stdio` (a Deployment attaches
  nothing to stdin) and validates OIDC prerequisites at template time; the
  `MCP_API_KEY` secretKeyRef is no longer `optional`, so a typo'd Secret fails
  pod creation with a clear event.

### Fixed
- Secrets (`MCP_API_KEY`, OIDC client secret/refresh token) are no longer
  echoed to stderr by `--help` or any mistyped flag: secret flags get empty
  defaults and resolve from the environment after parsing.
- `wardrowbe_create_item_from_url`'s description no longer instructs uploading
  local photos to a third-party host; the temp-host recipe lives only in the
  skill, next to its consent requirement, and the skill no longer instructs a
  nonexistent `material` parameter on `wardrowbe_update_item`.
- The release workflow's `publish` job can check out the repository
  (`contents: read` was missing, which fails while the repo is private).
- Image fetches retry once after a 401 (matching JSON calls), the decoded
  pixel-count guard multiplies in 64-bit (a crafted JPEG header could overflow
  32-bit `int` on the armv7 binary and bypass the decompression-bomb cap), and
  rotated phone JPEGs are no longer re-encoded sideways (EXIF orientation is
  detected; oriented originals are passed through).
- Caller-supplied data-URL MIME types are validated against a strict
  `image/<token>` pattern before being written into multipart headers
  (CR/LF header injection), and external image fetches share one transport
  with idle-connection bounds instead of leaking a keep-alive connection per
  call.
- `/readyz` no longer lets concurrent probes each ping the backend; the
  `Bearer` scheme is matched case-insensitively per RFC 7235 and compared via
  SHA-256 digests (removes the key-length timing side channel); panics no
  longer double-write response headers and `http.ErrAbortHandler` is
  re-panicked.
- Date arguments must be canonical `YYYY-MM-DD` (e.g. `2026-6-1` is rejected);
  outfit-name length is counted in characters, not bytes; unknown image
  variants are rejected instead of silently served as `medium`;
  `wardrowbe_get_outfit_images` caps garment fan-out at 20 and labels each
  image with its `item_id`.
- gosec download is checksum-verified in CI; Docker base images are pinned by
  digest; the GHCR helm-login token is passed via `env:`; `make run` guards
  its required variables and passes the API key via the environment;
  `make lint` fails on unformatted files; `.env.example` no longer carries
  inline comments that break `--env-file` parsing.

[Unreleased]: https://github.com/jansitarski/wardrowbe-mcp/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/jansitarski/wardrowbe-mcp/releases/tag/v1.0.0
