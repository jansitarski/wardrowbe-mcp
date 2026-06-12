# wardrowbe-mcp (Go)

[![CI](https://github.com/jansitarski/wardrowbe-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/jansitarski/wardrowbe-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jansitarski/wardrowbe-mcp?sort=semver)](https://github.com/jansitarski/wardrowbe-mcp/releases)
[![Go Version](https://img.shields.io/badge/go-1.25.11-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Image](https://img.shields.io/badge/ghcr.io-wardrowbe--mcp-blue?logo=docker)](https://github.com/jansitarski/wardrowbe-mcp/pkgs/container/wardrowbe-mcp)

A single static Go binary that exposes the
[Wardrowbe](https://github.com/Anyesh/wardrowbe) wardrobe API as tools for Claude.
Three capabilities make Claude a first-class part of the wardrobe:

- **Image view** — returns garment photos to Claude as MCP image content, so Claude's
  own vision tags and styles them instead of a small in-cluster model.
- **Tag / description write-back** — lets Claude save accurate attributes back to
  Wardrowbe, so it can correct what the auto-tagger got wrong.
- **Item creation** — lets Claude add a garment from an image:
  `wardrowbe_create_item_from_url` fetches a public image URL (SSRF-guarded), and
  `wardrowbe_create_item_from_base64` takes an inline image for local files. The
  backend stores and auto-tags it; Claude can then refine the tags via write-back.

It runs over Streamable HTTP (or stdio) and is designed to sit in a homelab k3s
cluster behind a Cloudflare Access MCP portal, used from Claude Desktop, Mobile, or
Code. Current version: **1.0.0**.

## Tools

32 tools covering the wardrobe API, all prefixed `wardrowbe_` so they stay unambiguous
in MCP hosts that aggregate several servers: browsing and analytics
(`wardrowbe_list_items`, `wardrowbe_get_item`, `wardrowbe_get_wardrobe_summary`, …),
wear/wash/archive lifecycle, outfit suggestion and feedback, the image-view tools
(`wardrowbe_get_item_image`, `wardrowbe_get_outfit_images`), write-back
(`wardrowbe_update_item`, `wardrowbe_set_item_tags`, `wardrowbe_set_item_description`),
and creation (`wardrowbe_create_outfit`, `wardrowbe_create_item_from_url`,
`wardrowbe_create_item_from_base64`, `wardrowbe_delete_outfit`).

Each tool maps to a Wardrowbe backend endpoint; the definitions live in
`internal/mcpserver/tools_*.go`.

## HTTP endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /` | none | Liveness — static `200`, no backend dependency. |
| `GET /readyz` | none | Readiness — pings the backend (3s); `503` when it's down. |
| `POST /mcp` | Bearer | The MCP endpoint (Streamable HTTP). |

`/mcp` is hardened for public exposure: a static bearer gate (constant-time, emits
RFC 9728 `WWW-Authenticate` on `401`), an inbound body-size cap, a concurrency
limiter that returns `503 Retry-After` when saturated, and panic recovery. Backend
error bodies are logged server-side only — never surfaced to the caller. Wire a k8s
`readinessProbe` to `/readyz` and `livenessProbe` to `/`.

## Quick start

Pull a pinned, prebuilt image (multi-arch, distroless, non-root) — the image and
Helm chart are published **public** on GHCR, so no auth is needed to pull them:

```bash
docker run --rm -p 8080:8080 -e MCP_API_KEY="$MCP_API_KEY" \
  ghcr.io/jansitarski/wardrowbe-mcp:1.0.0 \
  --transport http --host 0.0.0.0 --port 8080 \
  --wardrowbe-url http://backend.wardrowbe.svc.cluster.local:8000 \
  --auth dev --external-id <web-user-external-id> --external-email <real-email>
```

Or download a prebuilt static binary for your architecture (`amd64`, `arm64`, or
`armv7`) from the [latest release](https://github.com/jansitarski/wardrowbe-mcp/releases/latest)
— verify it against the published `SHA256SUMS.txt`:

```bash
curl -fL -o wardrowbe-mcp \
  https://github.com/jansitarski/wardrowbe-mcp/releases/download/v1.0.0/wardrowbe-mcp_1.0.0_linux_amd64
chmod +x wardrowbe-mcp
```

Or build from source (requires Go 1.25.11+, the version pinned in `go.mod`):

```bash
go build -o wardrowbe-mcp ./cmd/wardrowbe-mcp

# Pass the key via the environment, not argv — it would show up in `ps` output.
MCP_API_KEY="$MCP_API_KEY" ./wardrowbe-mcp \
  --transport http --host 0.0.0.0 --port 8080 \
  --wardrowbe-url http://backend.wardrowbe.svc.cluster.local:8000 \
  --auth dev --external-id <web-user-external-id> --external-email <real-email>
```

Or deploy to Kubernetes with the Helm chart (published as an OCI artifact per
release):

```bash
helm install wardrowbe-mcp \
  oci://ghcr.io/jansitarski/charts/wardrowbe-mcp --version 1.0.0 \
  -n wardrowbe --create-namespace \
  --set config.wardrowbeUrl=http://backend.wardrowbe.svc.cluster.local:8000 \
  --set apiKey.value="$MCP_API_KEY"
```

See [`charts/wardrowbe-mcp/`](charts/wardrowbe-mcp/README.md) for all values and a
Flux `HelmRelease` example.

Connect from Claude Code:

```bash
claude mcp add --transport http wardrowbe <url> --header "Authorization: Bearer $MCP_API_KEY"
```

## Configuration

Every flag has a matching `MCP_*` (or `WARDROWBE_URL`) environment variable; flags win.
The most-used ones:

| Flag | Default | Purpose |
|---|---|---|
| `--transport` | `http` | `http` (Streamable) or `stdio`. |
| `--wardrowbe-url` | — | Backend base URL (no `/api/v1`). **Required.** |
| `--api-key` | — | Incoming bearer key; **required for http**. Prefer `MCP_API_KEY` env over the flag (argv is visible in `ps`). |
| `--auth` | `dev` | `dev` or `oidc`. |
| `--external-id` / `--external-email` | — | Dev identity sent to `/auth/sync`. |
| `--oidc-token-endpoint` | — | Optional https token-endpoint override (skips OIDC discovery). |
| `--max-concurrent` | `16` | In-flight `/mcp` request cap. |
| `--max-body-mb` | `40` | Inbound `/mcp` body cap. |
| `--portal-resource-url` | — | Emits the RFC 9728 `resource_metadata` on `401`. |

Run `wardrowbe-mcp --help` for the complete flag list, including the image and OIDC
options. Every flag also has an `MCP_*` (or `WARDROWBE_URL`) environment variable;
see [`.env.example`](.env.example) for the full list. Secrets (`MCP_API_KEY`,
`MCP_OIDC_CLIENT_SECRET`, `MCP_OIDC_REFRESH_TOKEN`) are never echoed in `--help`
output or flag-error usage text.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `401 Unauthorized` on `/mcp` | Missing/incorrect bearer | Send `Authorization: Bearer $MCP_API_KEY`; confirm the value matches `--api-key`/`MCP_API_KEY`. |
| `503` on `/readyz` | Backend unreachable | Check `--wardrowbe-url` and that the backend is up; `/readyz` pings it. `/` (liveness) stays `200` regardless. |
| `503` with `Retry-After` on `/mcp` | Concurrency cap hit | Raise `--max-concurrent`; the limiter sheds load instead of queuing. |
| `413` / request rejected | Body exceeds cap | Raise `--max-body-mb` (default 40) for large base64 uploads. |
| `wardrowbe_create_item_from_url` refuses a URL | SSRF guard / non-public host | The URL must be `http(s)` and resolve to a public IP; private/loopback/link-local/multicast/NAT64 targets are blocked. |
| OIDC refresh fails with `invalid_grant` | Refresh token expired/rotated out | Issue a fresh refresh token. The server follows rotation automatically while running, but the rotated token is held in memory only — after a restart it resumes from the configured token, which a rotation-enforcing IdP may have invalidated. Re-issue and update `MCP_OIDC_REFRESH_TOKEN` whenever this happens; with such IdPs also run a single replica (a shared refresh token across replicas trips reuse detection). |
| Startup log warns about dev auth | `--auth dev` on http | Expected for a single user; set `--auth oidc` for real per-user identity. |
| Process exits at startup with "invalid …" | Bad flag/env value | The config is validated up front — the message names the offending flag/env var. |

## Development

```bash
make build   # static binary
make test    # go test -race ./...
make vet
make docker  # distroless, non-root image
```

Contributions: see [CONTRIBUTING.md](CONTRIBUTING.md). Security reports:
[docs/SECURITY.md](docs/SECURITY.md).

## Documentation

- [`docs/deployment.md`](docs/deployment.md) — running the server against a Wardrowbe
  backend: reference topology, backend dev auth, identity (`--external-id` /
  `--external-email`), the AI backend, and connecting Claude.
- [`docs/connecting-claude-via-cloudflare.md`](docs/connecting-claude-via-cloudflare.md)
  — exposing `/mcp` to Claude's native connectors through a Cloudflare tunnel + Access
  MCP portal: the required configuration and the client options.
- [`charts/wardrowbe-mcp/`](charts/wardrowbe-mcp/README.md) — the Helm chart:
  installable values, the API-key options, and a Flux `HelmRelease` example.
- [`skills/wardrowbe-image-upload/`](skills/wardrowbe-image-upload/SKILL.md) — a Claude
  Code skill for bulk-importing garment photos and giving them accurate tags with
  Claude's own vision instead of the backend's auto-tagger.

## Credits

Derived from [saya6k/mcp-wardrowbe](https://github.com/saya6k/mcp-wardrowbe) (MIT),
reimplemented in Go.

## License

[MIT](LICENSE).
