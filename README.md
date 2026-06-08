# wardrowbe-mcp (Go)

A single static Go binary that exposes the
[Wardrowbe](https://github.com/Anyesh/wardrowbe) wardrobe API as tools for Claude.
Three capabilities make Claude a first-class part of the wardrobe:

- **Image view** — returns garment photos to Claude as MCP image content, so Claude's
  own vision tags and styles them instead of a small in-cluster model.
- **Tag / description write-back** — lets Claude save accurate attributes back to
  Wardrowbe, so it can correct what the auto-tagger got wrong.
- **Item creation** — lets Claude add a garment from an image: `create_item_from_url`
  fetches a public image URL (SSRF-guarded), and `create_item_from_base64` takes an
  inline image for local files. The backend stores and auto-tags it; Claude can then
  refine the tags via write-back.

It runs over Streamable HTTP (or stdio) and is designed to sit in a homelab k3s
cluster behind a Cloudflare Access MCP portal, used from Claude Desktop, Mobile, or
Code. Current version: **0.3.0**.

## Tools

31 tools covering the wardrobe API: browsing and analytics (`list_items`, `get_item`,
`get_wardrobe_summary`, …), wear/wash/archive lifecycle, outfit suggestion and
feedback, the image-view tools (`get_item_image`, `get_outfit_images`), write-back
(`update_item`, `set_item_tags`, `set_item_description`), and creation
(`create_outfit`, `create_item_from_url`, `create_item_from_base64`, `delete_outfit`).

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

Pull a pinned, prebuilt image (multi-arch, distroless, non-root):

```bash
docker run --rm -p 8080:8080 -e MCP_API_KEY="$MCP_API_KEY" \
  ghcr.io/jansitarski/wardrowbe-mcp:0.3.0 \
  --transport http --host 0.0.0.0 --port 8080 \
  --wardrowbe-url http://backend.wardrowbe.svc.cluster.local:8000 \
  --auth dev --external-id <web-user-external-id> --external-email <real-email>
```

Or build from source:

```bash
go build -o wardrowbe-mcp ./cmd/wardrowbe-mcp

./wardrowbe-mcp \
  --transport http --host 0.0.0.0 --port 8080 \
  --wardrowbe-url http://backend.wardrowbe.svc.cluster.local:8000 \
  --auth dev --external-id <web-user-external-id> --external-email <real-email> \
  --api-key "$MCP_API_KEY"
```

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
| `--wardrowbe-url` | — | Backend base URL (no `/api/v1`). |
| `--api-key` | — | Incoming bearer key; **required for http**. |
| `--auth` | `dev` | `dev` or `oidc`. |
| `--external-id` / `--external-email` | — | Dev identity sent to `/auth/sync`. |
| `--max-concurrent` | `16` | In-flight `/mcp` request cap. |
| `--max-body-mb` | `40` | Inbound `/mcp` body cap. |
| `--portal-resource-url` | — | Emits the RFC 9728 `resource_metadata` on `401`. |

Run `wardrowbe-mcp --help` for the complete flag list, including the image and OIDC
options.

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
- [`skills/wardrowbe-image-upload/`](skills/wardrowbe-image-upload/SKILL.md) — a Claude
  Code skill for bulk-importing garment photos and giving them accurate tags with
  Claude's own vision instead of the backend's auto-tagger.

## Credits

Derived from [saya6k/mcp-wardrowbe](https://github.com/saya6k/mcp-wardrowbe) (MIT),
reimplemented in Go.

## License

[MIT](LICENSE).
