# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] - 2026-07-15

### Added
- Phase 2 external-tagging surface (for deployments where the internal vision
  model is off and an external agent owns tagging):
  - `wardrowbe_list_items` gains a `tagging_status` filter (`pending` | `tagged`),
    forwarded to `GET /items?tagging_status=…`, so the agent can ask "what still
    needs tagging?".
  - `wardrowbe_list_untagged_items` — convenience shorthand for the pending work
    queue (`tagging_status=pending`), mirroring `wardrowbe_get_items_to_wash`.
  - `wardrowbe_retag_item` — resets an item to the pending tagging queue
    (`POST /items/{id}/retag`), clearing its current tags' origin.
  - `auto_tag` boolean on both create tools (`wardrowbe_create_item_from_url`,
    `wardrowbe_create_item_from_base64`); set `false` to leave a new item pending
    for external tagging even when backend vision is enabled.
- `compact` boolean on `wardrowbe_get_recent_outfits`: returns a slim
  projection (outfit id/name/status/occasion/scheduled_for/created_at, items as
  id/type/name, plus total/has_more) instead of full outfit objects, which
  embed every item with signed image URLs and run to ~85k chars at limit 20.
  Use it for dedupe/overview checks; fetch details per outfit with
  `wardrowbe_get_outfit`.
- `--stateless` / `MCP_STATELESS` (default `true`): the Streamable HTTP
  transport now runs stateless by default — every POST is self-contained and
  no session id is issued. Set `false` to restore stateful sessions.

### Changed
- `wardrowbe_set_item_tags` sends a single `tags` payload; the backend
  (jansitarski/wardrowbe#2) projects every tag attribute (`colors`,
  `primary_color`, `pattern`, `material`, `style`, `season`, `formality`) onto
  its first-class column on write-back, so agent-set values are visible to
  column-based filters and scoring without any top-level duplication. Its
  description also now documents the replace semantics: a call replaces the
  item's full attribute set, so include every attribute you want to keep.
  The backend additionally requires a write-back to carry content — a call with
  only empty values leaves the item pending (this tool already rejects
  attribute-less calls client-side) — and `GET /capabilities` now advertises
  `features.external_tagging: true` (not yet consumed by this server).

### Fixed
- Intermittent `MCP server connection lost` errors from the claude.ai connector
  (typically on the first call after idle or one of two concurrent calls, with
  an immediate retry succeeding). Three transport-lifecycle changes:
  - The Streamable HTTP server now sends a heartbeat every 25s on any standing
    GET (listening) stream, so intermediaries that kill quiet streamed
    responses (Cloudflare drops them after ~100s without bytes) no longer
    silently sever the connection the client discovers dead on its next call.
  - The server runs **stateless** by default (see `--stateless` above): every
    POST is self-contained, so a pod restart can no longer invalidate a
    session the connector still holds.
  - Removed the http.Server `WriteTimeout` (was 6m): it spans the entire
    response lifetime, so it hard-killed even actively heartbeating streams at
    exactly that mark. Handler work stays bounded by the backend client's
    5-minute timeout.

### Removed
- The `/mcp` concurrency limiter and its `--max-concurrent` / `MCP_MAX_CONCURRENT`
  knob (and the chart's `config.maxConcurrent` value). The limiter counted every
  in-flight request — including the long-lived SSE streams the Streamable HTTP
  transport holds open — so a single user with a few reconnecting sessions could
  exhaust the 16 slots and get spurious `503 server at capacity` responses. For
  a single-user deployment the cap causes more harm than the OOM pile-up it
  guarded against; backend connections remain bounded by the HTTP transport's
  per-host connection ceiling.

### Security
- Bumped the Go toolchain to 1.25.12 (go.mod, CI, release, Docker base image):
  1.25.11's `crypto/tls` carries a known vulnerability, fixed in 1.25.12. No
  code changes.

## [1.0.4] - 2026-06-15

### Added
- `--oidc-refresh-token-file` / `MCP_OIDC_REFRESH_TOKEN_FILE`: a path (not a
  secret) on a persistent volume where the rotated refresh token is persisted
  and reloaded across restarts. Required for IdPs that rotate refresh tokens
  single-use (e.g. Cloudflare Access), which would otherwise invalidate the
  configured seed after the first refresh and break auth on every pod restart.
  The latest rotation is loaded on startup (in preference to the seed) and each
  rotation is written back atomically (temp file + `fsync` + rename, `0600`); a
  pre-populated file alone can bootstrap the refresh grant without a seed.

## [1.0.3] - 2026-06-15

### Fixed
- OIDC `/auth/sync` no longer fails with HTTP 422 when the `id_token` omits the
  `name` claim. The backend requires a non-empty `display_name`, but `name` is
  optional and some issuers omit it (e.g. Cloudflare Access id_tokens minted via
  the `refresh_token` grant carry only `sub`). `display_name` now falls back
  `name` → `email` → `sub`, with each candidate trimmed so a whitespace-only
  claim can't slip through as a blank name.

## [1.0.2] - 2026-06-15

### Added
- `--oidc-id-token` / `MCP_OIDC_ID_TOKEN`: a static OIDC `id_token` source for
  issuers that do not issue refresh tokens. Treated as a secret (env-only
  default, never echoed in `--help` or flag-error usage).

### Changed
- OIDC mode now forwards the raw `id_token` in the `/auth/sync` body so a backend
  running in OIDC mode validates it against the issuer's JWKS. Previously only the
  projected `external_id` / `email` / `display_name` were sent, which an
  OIDC-validating backend rejected with `401` — the connector could not
  authenticate at all. Local claim decoding is kept only to populate the
  convenience fields.
- The refresh-token grant is now optional. OIDC mode accepts **either**
  `--oidc-refresh-token` (durable — a fresh `id_token` is minted per sync) **or**
  `--oidc-id-token` (static fallback). `--oidc-issuer-url` / `--oidc-client-id`
  are required only on the refresh-token path; the static path never contacts the
  issuer.

### Fixed
- A statically configured `id_token` that has already expired now fails fast with
  an actionable error instead of being forwarded, which would otherwise re-sync
  against `/auth/sync` on every request with no backoff.

## [1.0.1] - 2026-06-13

### Added
- `wardrowbe_download_image` tool — fetches a Wardrowbe-hosted image by a
  reference already in context (an item's `image_url`/`medium_url`/
  `thumbnail_url`, an outfit image, or an `additional_images` entry) and returns
  it inline so it renders in the conversation. Unlike `wardrowbe_get_item_image`
  (which takes an `item_id` and does a `GET /items/{id}` first), it accepts a
  relative `/api/v1/images/...` path or a full backend URL and fetches it over
  the *authenticated* backend connection — so it works even when the backend
  sits behind a Cloudflare Access tunnel where a direct, unauthenticated URL
  fetch would be bounced to a login page. The reference is validated before
  dialing (host must match the backend; path must be under `/api/v1/images/`)
  so the bearer-bearing fetch can't be turned into a proxy to other hosts or
  endpoints.

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
  given (was: returned the entire wardrobe), and echoes the effective
  `page`/`page_size` around the backend payload (under `result`) so a
  truncated listing is observable even when the backend response carries no
  pagination metadata.
- `--wardrowbe-url` no longer silently defaults to `http://127.0.0.1:8000`; it
  is required and startup fails with a clear message when missing.
- OIDC: the discovered `token_endpoint` is no longer pinned to the issuer host
  (https is still required) — fixes Google and AWS Cognito, whose token
  endpoints live on a different domain. A cross-host endpoint is logged at
  startup so the expanded trust is visible; a new `--oidc-token-endpoint`
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
- **Breaking:** tool arguments are validated strictly against their declared
  schema types. String-encoded numbers and booleans (`page_size: "50"`,
  `favorite: "true"`) that earlier releases coerced are now rejected with a
  clear error — clients must send schema-typed values. Likewise,
  present-but-uncoercible arguments (`favorite: "yes"`, `rating: "high"`) are
  rejected instead of silently writing defaults; out-of-range ratings are
  rejected instead of clamped; empty/whitespace IDs are rejected (previously
  `get_item("")` returned the whole collection); `archive_item` enforces the
  backend's 50-char reason limit with a clear error.
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
  rotated phone JPEGs are no longer re-encoded sideways: the EXIF orientation
  is baked into the pixels before downscaling, so oriented photos come back
  both upright and small.
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
