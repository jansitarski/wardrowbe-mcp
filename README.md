# wardrowbe-mcp (Go)

A Go reimplementation of the [Wardrowbe](https://github.com/Anyesh/wardrowbe) MCP
server (originally [`saya6k/mcp-wardrowbe`](https://github.com/saya6k/mcp-wardrowbe),
Python/FastMCP), with **feature parity** plus two new capabilities:

1. **Image view** — return garment photos to Claude as MCP image content, so Claude's
   own vision does analysis/styling instead of a small in-cluster vision model.
2. **Tag / description write-back** — let Claude save accurate attributes back to
   Wardrowbe (`PATCH /items/{id}`).

It exposes the Wardrowbe wardrobe API as MCP tools over Streamable HTTP, intended to
run in a homelab k3s cluster behind a Cloudflare Access MCP portal and used from
Claude (Desktop / Mobile / Code).

## Status

Implemented (v0.3.0). Single static Go binary exposing **31 MCP tools** (22 parity
+ `get_item_image`, `get_outfit_images`, `update_item`, `set_item_tags`,
`set_item_description`, `create_outfit` — compose an outfit from chosen item ids via
`POST /outfits/studio` — `delete_outfit` to remove any outfit by id,
`create_item_from_url` — add a garment from a public image URL, SSRF-guarded, which
the server uploads to `POST /items` for backend auto-tagging — and
`create_item_from_base64` — add a garment from an inline base64 image (raw or data
URL) for when the photo is local and has no public URL) over Streamable HTTP
and stdio. `go test -race ./...` green.

### HTTP endpoints & production hardening (v0.3.0)

- `GET /` — **liveness** (static; process is up, no backend dependency).
- `GET /readyz` — **readiness** (pings the backend within 3s; `503` when it's down).
  Wire k8s `readinessProbe` here and `livenessProbe` to `/`.
- `POST /mcp` — bearer-gated MCP endpoint, wrapped with an inbound body-size cap
  (`--max-body-mb`, default 40) and a concurrency limiter (`--max-concurrent`,
  default 16; returns `503 Retry-After` when saturated). A panic in any layer is
  recovered to a clean `500`. Backend error bodies are logged server-side (debug)
  and never surfaced to the MCP caller. Image fetches/decodes are size- and
  pixel-capped against decompression bombs.

```bash
go test -race ./...        # unit tests (config, client retry, image, auth gate)
go build ./cmd/wardrowbe-mcp
docker build -t wardrowbe-mcp .
```

## Docs

- [`docs/go-rewrite-spec.md`](docs/go-rewrite-spec.md) — the implementation spec:
  config surface, the 22 parity tools → backend endpoints, the new image + write-back
  tools, auth model, Dockerfile, deployment, and a rollout/parity checklist.
- [`docs/context.md`](docs/context.md) — background from the homelab bring-up: the
  deployment topology, the Cloudflare Access/OAuth setup and its gotchas, the vision-model
  evaluation, the identity/`external_id` model, the backend API surface, and the bugs
  this rewrite is meant to fix.

## Why a rewrite

Beyond a smaller single-binary image, the Go version fixes three issues found while
running the Python server in production (see `docs/context.md`):

1. `health` probes the wrong path (`/health` 404s; should be `/api/v1/health`).
2. The 401 on `/mcp` omits `WWW-Authenticate`, which Claude's connector needs to
   discover OAuth (worked around today with a Cloudflare Transform Rule).
3. The dev `/auth/sync` derives `<external-id>@wardrowbe.local` as the email, causing
   the account email to flap; a new `--external-email` flag fixes it.

## Quick start (planned)

```bash
go build -o wardrowbe-mcp ./cmd/wardrowbe-mcp
./wardrowbe-mcp \
  --transport http --host 0.0.0.0 --port 8080 \
  --wardrowbe-url http://backend.wardrowbe.svc.cluster.local:8000 \
  --auth dev --external-id <web-user-external-id> --external-email <real-email> \
  --api-key "$MCP_API_KEY"
```

To emit the RFC 9728 `WWW-Authenticate` header natively (retiring the Cloudflare
Transform Rule), pass `--portal-resource-url`:

```bash
  --portal-resource-url https://<your-portal-host>/.well-known/oauth-protected-resource
```

## License

[MIT](LICENSE).
