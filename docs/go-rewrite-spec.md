# Wardrowbe MCP — Go reimplementation spec

A from-scratch Go rewrite of [`saya6k/mcp-wardrowbe`](https://github.com/saya6k/mcp-wardrowbe)
(Python/FastMCP) with **feature parity** plus two new capabilities:

1. **Image view** — return garment photos to Claude as MCP image content, so
   Claude's own vision does analysis/styling instead of the weak in-cluster VLM.
2. **Tag / description write-back** — let Claude save accurate attributes back to
   Wardrowbe (`PATCH /items/{id}`), effectively replacing the local vision model
   for tagging.

Target deploy: drop-in replacement for the current `wardrowbe-mcp` Deployment in
`apps/wardrowbe-mcp/` (same Service `:8080`, same Cloudflare Access portal).

---

## 1. Why Go (and what we fix along the way)

- **Single static binary**, `scratch`/`distroless` image ≈ 15–25 MB vs the current
  `python:3.12-slim + pip` image (~200 MB+). Faster cold start, smaller attack surface.
- **No `pip install git+…` at build time** — `go build` produces a self-contained binary.
- Opportunities to fix three things we hit during bring-up:
  - **`health` false-negative.** The Python client probes `GET /health`, which 404s;
    the real endpoint is `GET /api/v1/health` (returns `{"status":"healthy"}`).
    The Go version probes the correct path.
  - **Missing `WWW-Authenticate` on 401.** We had to add a Cloudflare Transform Rule
    to inject it. The Go server emits a spec-correct
    `WWW-Authenticate: Bearer resource_metadata="…"` on 401, so that workaround can
    be retired (keep it harmlessly, or remove once verified).
  - **Dev-sync email flap.** Add an explicit `MCP_EXTERNAL_EMAIL` so dev `/auth/sync`
    can send the real `jaansi2016@gmail.com` instead of `<external-id>@wardrowbe.local`,
    eliminating the email field flapping between web and MCP logins.

---

## 2. Architecture

```
Claude (Desktop/Mobile/Code)
   │  MCP over Streamable HTTP  (+ optional SSE)
   ▼
[ Go MCP server :8080 ]
   ├─ incoming auth: Bearer MCP_API_KEY on /mcp + /sse  (anon 200 on /)
   ├─ MCP layer: tools (+ image content results), resources
   └─ Wardrowbe API client
        ├─ backend auth: dev /auth/sync (or OIDC refresh) → JWT, cached
        └─ HTTP → backend.wardrowbe.svc.cluster.local:8000/api/v1/*
```

Two independent auth boundaries (unchanged from the Python design):

| Boundary | Mechanism |
|---|---|
| **Client → MCP** (`/mcp`, `/sse`) | static `Authorization: Bearer <MCP_API_KEY>` |
| **MCP → backend** | dev `/auth/sync` JWT, or OIDC refresh-token → id_token → `/auth/sync` |

The public edge stays the Cloudflare Access MCP portal (OAuth); the origin keeps
the static bearer. No change to that topology.

---

## 3. Configuration surface

Mirror the Python flags/env, plus the new ones. Flags override env.

| Flag | Env | Default | Notes |
|---|---|---|---|
| `--transport` | `MCP_TRANSPORT` | `http` | `http` (Streamable) or `stdio` |
| `--host` | `MCP_BIND_HOST` | `0.0.0.0` | |
| `--port` | `MCP_BIND_PORT` | `8080` | |
| `--wardrowbe-url` | `WARDROWBE_URL` | `http://127.0.0.1:8000` | backend base (no `/api/v1`) |
| `--api-key` | `MCP_API_KEY` | — | **required for http**; incoming Bearer |
| `--auth` | `MCP_AUTH_MODE` | `dev` | `dev` or `oidc` |
| `--external-id` | `MCP_EXTERNAL_ID` | `wardrowbe-mcp` | dev identity (must match the web user's `external_id`, e.g. `jaansi2016-gmail-com`) |
| `--external-email` | `MCP_EXTERNAL_EMAIL` | `<external-id>@wardrowbe.local` | **NEW** — send the real email in dev sync to stop the email flap |
| `--oidc-issuer-url` | `MCP_OIDC_ISSUER_URL` | — | oidc mode |
| `--oidc-client-id` | `MCP_OIDC_CLIENT_ID` | — | oidc mode |
| `--oidc-client-secret` | `MCP_OIDC_CLIENT_SECRET` | — | oidc mode |
| `--oidc-refresh-token` | `MCP_OIDC_REFRESH_TOKEN` | — | oidc mode |
| `--log-level` | `MCP_LOG_LEVEL` | `INFO` | |
| `--image-max-dim` | `MCP_IMAGE_MAX_DIM` | `768` | **NEW** — cap returned image dimension for token economy |
| `--image-default-variant` | `MCP_IMAGE_VARIANT` | `medium` | **NEW** — `thumb`/`medium`/`full` |

---

## 4. Backend auth client (parity)

Port the Python `WardrowbeClient` semantics exactly:

- **JWT cache** with TTL from `expires_in` minus a refresh leeway; guarded by a mutex.
- `_request`: attach `Authorization: Bearer <jwt>`; on **401, re-sync once** (force a
  fresh `/auth/sync`) and retry; on a second 401 → auth error. `204` → nil body.
- **`POST /api/v1/auth/sync`** body (dev): `{ "external_id", "email", "display_name" }`
  → response `{ "access_token", "expires_in" }`.
- **OIDC mode**: discover token endpoint from issuer `/.well-known/openid-configuration`,
  exchange refresh_token → id_token, then `/auth/sync` with claims (`sub`, `email`,
  `name`). Cache + refresh.

```go
type TokenProvider interface { SyncPayload(ctx context.Context) (SyncPayload, error) }
type SyncPayload struct { ExternalID, Email, DisplayName string }
// DevTokenProvider{externalID, email, displayName}  // email defaults to <id>@wardrowbe.local
// OIDCTokenProvider{issuer, clientID, clientSecret, refreshToken, httpClient}
```

---

## 5. Tool catalog — parity (22 tools)

`apiBase = "/api/v1"`. Each tool returns JSON text content unless noted.

| Tool | Method | Backend path | Params |
|---|---|---|---|
| `health` | GET | `/api/v1/health` *(fixed path)* | — |
| `auth_config` | GET | `/auth/config` | — |
| `session_info` | GET | `/auth/session` | — |
| `get_wardrobe_summary` | GET | `/analytics` | — |
| `get_most_worn_items` | GET | `/analytics` (derived) | `limit 1–10` |
| `list_items` | GET | `/items` | `page, page_size, category, is_archived, needs_wash, search` |
| `get_item` | GET | `/items/{id}` | `item_id` |
| `get_items_to_wash` | GET | `/items?needs_wash=true` | `limit 1–20` |
| `suggest_outfit` | POST | `/outfits/suggest` | `occasion, time_of_day, target_date, notes` (validate enums) |
| `get_latest_outfit` | GET | `/outfits` (first) | — |
| `get_outfit` | GET | `/outfits/{id}` | `outfit_id` |
| `get_recent_outfits` | GET | `/outfits` | `limit 1–20, status?` |
| `accept_latest_outfit` | POST | `/outfits/{id}/accept` | — |
| `reject_latest_outfit` | POST | `/outfits/{id}/reject` | — |
| `skip_latest_outfit` | POST | `/outfits/{id}/skip` | — |
| `submit_outfit_feedback` | POST | `/outfits/{id}/feedback` | `rating 1–5, wore bool, notes` |
| `log_wear` | POST | `/items/{id}/wear` | `item_id, date? (YYYY-MM-DD)` |
| `log_wash` | POST | `/items/{id}/wash` | `item_id` |
| `archive_item` | POST | `/items/{id}/archive` | `item_id, reason?` |
| `restore_item` | POST | `/items/{id}/restore` | `item_id` |
| `recent_notifications` | GET | `/notifications/history` | `limit 1–100` |
| `test_notification` | POST | `/notifications/settings/{id}/test` | `setting_id` |

Enums to validate server-side (from upstream `server.py`):
- `occasion`: beach, brunch, business-casual, casual, date, dinner, formal, gym,
  hiking, interview, lounge, office, outdoor, party, running, smart-casual, sport,
  sporty, travel, wedding, weekend, work
- `time_of_day`: morning, afternoon, evening, night, full day

List responses come back as `{items|results|data|outfits|notifications: [...]}` or a
bare array — coerce to a list (port the `_coerce_list` helper).

---

## 6. NEW — Image view

Backend serves bytes at:

```
GET /api/v1/images/{user_id}/{filename}?expires=<ts>&sig=<hmac>   → image bytes
```

Item/outfit payloads carry `image_path`, `thumbnail_path`, `medium_path`, and
list responses include pre-signed `thumbnail_url` (cluster-internal). The MCP holds
an authed session, so it fetches bytes server-side and returns them as MCP image
content (base64). Claude renders/sees them.

**Client method**

```go
// variant: "thumb" | "medium" | "full"
func (c *Client) ItemImage(ctx context.Context, itemID, variant string) (data []byte, mime string, err error)
//   1. GET /items/{id}  → pick {thumbnail,medium,image}_path by variant
//   2. GET /images/{user_id}/{filename}   (signed URL from payload, or authed)
//   3. read bytes; sniff mime (image/jpeg|png|webp)
//   4. optionally downscale to --image-max-dim (token economy)
```

**Tools**

| Tool | Returns | Params |
|---|---|---|
| `get_item_image` | image content | `item_id`, `variant? (default MCP_IMAGE_VARIANT)` |
| `get_outfit_images` | one image per garment + a small JSON manifest | `outfit_id`, `variant?` |

Return shape (MCP image content block):

```jsonc
{ "type": "image", "data": "<base64>", "mimeType": "image/jpeg" }
```

**Token economy (important):** default to `medium`/`thumb`, never auto-return all 27
items. `get_outfit_images` returns only the garments in that outfit. Cap dimension at
`--image-max-dim`. Document that each image costs vision tokens.

---

## 7. NEW — Tag / description write-back

Backend update endpoint:

```
PATCH /api/v1/items/{item_id}        body = ItemUpdate (all fields optional)
```

`ItemUpdate` (from `backend/app/schemas/item.py`):

```jsonc
{
  "type": "shirt", "subtype": "...", "name": "...", "brand": "...",
  "notes": "free text",            // use for description write-back
  "favorite": true,
  "colors": ["blue"], "primary_color": "blue",
  "wash_interval": 5,
  "tags": {                         // ItemTags — the structured attributes
    "colors": ["blue"], "primary_color": "blue",
    "pattern": "solid", "material": "linen",
    "style": ["smart-casual"], "season": ["spring","summer"],
    "formality": "smart-casual", "fit": "regular"
  }
}
```

**Client method**

```go
func (c *Client) UpdateItem(ctx context.Context, itemID string, patch ItemUpdate) (Item, error) // PATCH
```

**Tools**

| Tool | Maps to | Params |
|---|---|---|
| `update_item` | `PATCH /items/{id}` | `item_id` + any of: `type, subtype, name, brand, notes, favorite, primary_color, colors[], wash_interval` |
| `set_item_tags` | `PATCH /items/{id}` (`tags` only) | `item_id, tags{colors,primary_color,pattern,material,style[],season[],formality,fit}` |
| `set_item_description` | `PATCH /items/{id}` (`notes`) | `item_id, description` |

**The payoff workflow** (Claude as the tagger):
1. `list_items` → pick an item with poor/empty tags.
2. `get_item_image(item_id)` → Claude *sees* the garment.
3. Claude infers attributes → `set_item_tags` / `set_item_description` writes them back.

This makes the local `qwen2.5vl:3b` optional — keep it only for the backend's
own upload-time auto-tag, or drop vision entirely and let Claude tag on demand.

> Note: there is **no** item *create* endpoint exposed for MCP here — uploads still
> happen in the web UI (multipart). Write-back edits existing items. Adding create
> would require multipart upload of image bytes (out of scope for v1; see §12).

---

## 8. Recommended Go libraries

- **MCP SDK**: [`github.com/mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go)
  — mature, supports tools, resources, **image content results**, and Streamable
  HTTP + SSE servers. (Alternative: the official
  [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
  — newer; verify image-content + Streamable-HTTP support before choosing.)
- **HTTP**: stdlib `net/http` (client with timeouts; server with middleware).
- **Image downscale** (optional): `golang.org/x/image` or `github.com/disintegration/imaging`.
- **Logging**: stdlib `log/slog`.
- **Config**: stdlib `flag` + `os.Getenv` (no framework needed).

Suggested layout:

```
cmd/wardrowbe-mcp/main.go        # flag/env parse, wire transport + server
internal/config/config.go
internal/wardrowbe/client.go     # API client + JWT cache + _request retry
internal/wardrowbe/auth.go       # Dev + OIDC token providers
internal/wardrowbe/types.go      # Item, ItemUpdate, ItemTags, Outfit, SyncPayload
internal/mcpserver/server.go     # build server, register tools
internal/mcpserver/tools_*.go    # items / outfits / images / writeback / notifications
internal/mcpserver/middleware.go # bearer gate, anon "/", WWW-Authenticate on 401
```

---

## 9. Transport & Cloudflare notes

- **Streamable HTTP** at `POST /mcp` (require `Accept: application/json, text/event-stream`).
- **Anon `GET /`** → 200 (readiness/liveness probe; no auth).
- **`GET /sse`** optional (legacy SSE) — only if a client needs it.
- **401 on `/mcp` without bearer** → include
  `WWW-Authenticate: Bearer resource_metadata="https://<portal-host>/.well-known/oauth-protected-resource"`.
  Emitting this natively means the Cloudflare Transform Rule we added can be removed.
- Cloudflare Access still fronts OAuth; the portal injects the static bearer to this
  origin (unchanged). The portal's **Allowed redirect URLs** must still list
  `https://claude.ai/api/mcp/auth_callback` and `https://claude.com/api/mcp/auth_callback`,
  and the geo-skip WAF rule must cover the hostnames — all unchanged from today.

---

## 10. Dockerfile (multi-stage, non-root, scratch)

```dockerfile
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/wardrowbe-mcp ./cmd/wardrowbe-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wardrowbe-mcp /wardrowbe-mcp
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/wardrowbe-mcp"]
```

Build it in `build/build-images.sh` from your fork's git ref (same `MCP_REF`
pattern), tag `ghcr.io/jansitarski/wardrowbe-mcp-go:<ver>`, keep it private (the
existing `ghcr-pull` secret already covers the namespace).

---

## 11. Deployment changes

`apps/wardrowbe-mcp/deployment.yaml`: swap the image to the Go build; args/env are a
superset of today's:

```yaml
args:
  - --transport=http
  - --host=0.0.0.0
  - --port=8080
  - --wardrowbe-url=http://backend.wardrowbe.svc.cluster.local:8000
  - --auth=dev
  - --external-id=jaansi2016-gmail-com
  - --external-email=jaansi2016@gmail.com      # NEW — stops email flap
env:
  - { name: MCP_API_KEY, valueFrom: { secretKeyRef: { name: wardrowbe-mcp-secrets, key: mcp-api-key } } }
  - { name: MCP_LOG_LEVEL, value: "INFO" }
```

Service `:8080`, probes on `/`, and the resource limits stay as-is. No portal/DNS
changes.

---

## 12. Rollout & parity checklist

1. Build the Go image; deploy behind a **temporary** second Service or just swap the
   image and watch logs.
2. **Parity tests** (all 22 tools) — `health` now returns `{healthy:true}`; spot-check
   `get_wardrobe_summary` (27 items), `list_items`, `suggest_outfit`, `log_wear`/`log_wash`.
3. **Image tests** — `get_item_image` returns a viewable photo; `get_outfit_images`.
4. **Write-back tests** — `set_item_tags` then `get_item` shows the new tags;
   confirm the web UI reflects them.
5. Re-run the Cloudflare smoke test (anon `/`→200, `/mcp` no-bearer→401 **with**
   `WWW-Authenticate`, `/mcp`+bearer→`initialize`).
6. Confirm in Claude (Code + mobile via portal) that tools + images appear.

## Open questions / v-next

- **Item create via MCP** (multipart image upload) — would let Claude add garments,
  not just edit. Bigger; needs `POST /items` multipart. Defer to v2.
- **Resources vs tools for images** — could also expose `image://item/{id}` MCP
  resources for browseable galleries; tools are simpler for on-demand analysis.
- **Bulk re-tag** — a helper that walks items with empty tags and asks Claude to tag
  each from its image (client-driven loop, not a single tool).
- Pick the MCP Go SDK and confirm its image-content + Streamable-HTTP support before
  committing the project layout.
```
