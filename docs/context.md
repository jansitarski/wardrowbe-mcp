# Project context — how this came to be

This Go rewrite grew out of deploying the **Python** Wardrowbe MCP
(`saya6k/mcp-wardrowbe`) into a k3s homelab and hitting a series of limitations.
This document captures the background and hard-won findings so the rewrite has full
context without re-deriving any of it.

---

## The system

- **Wardrowbe** (`Anyesh/wardrowbe`) — self-hosted wardrobe catalog. Components:
  Postgres + Redis + **FastAPI backend** + **arq worker** + **Next.js frontend**.
  Upstream publishes no images; they're built from source.
- **Homelab** (`k8s-homelab` repo) — k3s, **Flux** GitOps, **SOPS** (age) secrets.
  Two relevant namespaces: `wardrowbe`, `ollama`. App images live at
  `ghcr.io/jansitarski/wardrowbe-{backend,frontend,mcp}` — **private**, pulled with a
  `dockerconfigjson` secret `ghcr-pull` in the `wardrowbe` namespace (created
  imperatively from a `gh` token; not stored in Git).
- **Design principle:** **Claude is the styling reasoning engine** (Pro/Max custom
  connector). No external LLM API keys anywhere. Wardrowbe's "AI required" gate is
  satisfied by a tiny **in-cluster Ollama** (text + vision), which is intentionally
  mediocre — the MCP hands its output to Claude, which does the real reasoning.

## Topology / service addresses

| Component | In-cluster address | Notes |
|---|---|---|
| Backend (FastAPI) | `backend.wardrowbe.svc.cluster.local:8000` | API under `/api/v1` |
| MCP server | `wardrowbe-mcp.wardrowbe.svc.cluster.local:8080` | `--auth dev`, bearer-protected `/mcp` |
| Frontend (Next.js) | `frontend.wardrowbe.svc.cluster.local:3000` | NextAuth at `/api/auth/*` |
| Ollama | `ollama.ollama.svc.cluster.local:11434` | OpenAI-compatible `/v1`; models on PVC |
| Web UI ingress | `http://wardrowbe.local` (nginx, internal-only) | LAN DNS / hosts entry → node `192.168.2.200` |

**Ingress path routing** (mirrors upstream `nginx/templates/default.conf.template`):
`/api/auth → frontend`, `/api → backend`, `/ → frontend`; plus `proxy-body-size: 50m`
(uploads) and `proxy-read/send-timeout: 300` (Ollama outfit suggestions). Getting this
wrong (sending all `/api` to the backend) breaks NextAuth login with a FastAPI
`{"detail":"Not Found"}`.

## Auth model (important and subtle)

Two independent boundaries:

- **Client → MCP**: static `Authorization: Bearer <MCP_API_KEY>` on `/mcp`.
- **MCP → backend**: dev `/auth/sync` (or OIDC) → JWT (cached, refreshed on 401).

**Dev identity / `external_id`:**
- Web dev login (`DevCredentialsProvider`) requires **`DEV_MODE=true` on the
  frontend** (the prod build is `NODE_ENV=production`; `DEBUG=true` is the *backend*
  flag and does NOT enable the frontend provider). Email → `external_id` is normalized
  by replacing `@`/`.` with `-`: `user@example.com` → **`your-user-id`**.
- Backend dev login: `DEBUG=true` + `SECRET_KEY=change-me-in-production`.
  `POST /api/v1/auth/sync {external_id,email,display_name}` → `{access_token,expires_in}`.
  Lookup is **by `external_id` first**; if missing, fall back to email-match migration;
  in the found-branch it **overwrites `user.email`** with the synced email.
- **MCP must use the web user's `external_id`** to share the wardrobe. The Python dev
  provider derives email as `<external-id>@wardrowbe.local` with no override → the
  account email **flaps** between the real address and the `.local` form depending on
  who synced last. Harmless (items key on `user_id`, `external_id` is the stable join
  key) but ugly → the Go rewrite adds `--external-email`.
- **Items follow `user_id`**, not email. There is no item *create* endpoint exposed to
  the MCP — uploads happen in the web UI (multipart).

## Cloudflare exposure (the hard part)

`cloudflared` is a **token-tunnel** (dashboard-managed hostnames). Two hostnames:
- **Origin**: `wardrowbe-mcp.sitarski.tech` → MCP `:8080` (bearer-protected).
- **Portal**: `wardrowbe-portal.sitarski.tech` → a **Cloudflare Access MCP server
  portal** that adds the **OAuth** the Connectors UI requires and injects the static
  bearer to the origin. Auth server: `https://sitarski.cloudflareaccess.com`.

The Connectors UI (Desktop/Mobile/web) does **not** accept a static bearer — it needs
server-side OAuth. **Four fixes were required** to make the native connector work:

1. **Geo WAF rule** on `sitarski.tech` blocked everything outside Poland → **403** for
   Cloudflare's MCP control plane *and* Anthropic's cloud (both non-Poland). Fix: a WAF
   **Skip** rule for the `wardrowbe-*` hostnames, above the geo rule.
2. **Tunnel service URL typo** (`svc.cluster.local.local`) → **502** (`no such host`).
3. **Portal `/mcp` 401 omitted `WWW-Authenticate`** → Claude couldn't discover OAuth and
   dead-ended on claude.ai. Fix: a **Transform Rule** injecting
   `WWW-Authenticate: Bearer resource_metadata="https://wardrowbe-portal.sitarski.tech/.well-known/oauth-protected-resource"`
   on 401. *(The Go server can emit this natively and retire the rule.)*
4. **Access OAuth app rejected `claude.ai`/`claude.com` redirect URIs** (it allows only
   `localhost`, which is why `mcp-remote` works on Desktop). Fix: add both
   `https://claude.ai/api/mcp/auth_callback` and `https://claude.com/api/mcp/auth_callback`
   to the app's **Allowed redirect URLs**. Cloudflare Access also requires the
   **RFC 8707 `resource`** parameter, which Claude sends automatically.

Also: Access **policies** must `Allow` + `Include` the user's email; the default is
deny → 403 at "Save and connect". Client options:
- **Claude Code (CLI)**: static bearer works — `claude mcp add --transport http <url> --header "Authorization: Bearer <key>"`.
- **Desktop config-file**: `mcp-remote` brokers the Access OAuth via a `localhost` redirect.
- **Desktop/Mobile Connectors UI**: the portal + the four fixes above.

## AI / vision models (evaluated on a real garment)

Test image: a solid blue **linen** Hackett button-up (point collar, full placket, small
green chest logo), flat product shot.

| Model | Size | Verdict |
|---|---|---|
| `moondream` | 1.7 GB | Hallucinated **white collar/cuffs**, **mannequin**, **cotton**. Too weak. |
| `llava:7b` | 4.7 GB | Got button-up/blue, but **white collar**, **denim**, **hanger/dark bg**, **missed the logo**. Fit 8Gi. |
| `qwen2.5vl:7b` | 6.0 GB | **OOM** at the 8Gi limit (needs ~10–11 GB; node is 16 GB, no GPU). Unusable here. |
| **`qwen2.5vl:3b`** | 3.2 GB | **Winner.** Correct color, **no** white-collar/mannequin hallucination, **caught the green logo**. Fits 8Gi. Errors: cotton vs linen (unavoidable from a photo), collar style slightly off, a "three buttons" miscount in one run. |

**Conclusions:**
- Small CPU VLMs are *rough-draft* taggers; **fabric (linen vs cotton) is unreliable**
  from a photo even for larger models. This is the core motivation for the **image-view
  + write-back** features — let Claude's own vision tag accurately.
- Active config: `AI_VISION_MODEL=qwen2.5vl:3b`, `AI_TEXT_MODEL=qwen2.5:1.5b`.
- The text model only needs to emit **valid JSON** for the recommender (`"Respond with
  valid JSON:"` + tolerant parser); quality is irrelevant since Claude restyles.
- Ollama Deployment bumped to **8Gi / 4 CPU**. CPU-only → ~2–4 min/image.
- The MCP `health` tool reports `healthy:false` only because it probes `/health`
  (404); the real endpoint is `/api/v1/health` (`{"status":"healthy"}`).

## Backend API surface (for the Go client)

Base `/api/v1`. Endpoints used / needed:

- Auth: `GET /auth/config`, `GET /auth/session`, `POST /auth/sync`.
- Health: `GET /health` (note: backend mounts it under `/api/v1/health`).
- Analytics: `GET /analytics` (wardrobe summary, most/least/never worn, distributions).
- Items: `GET /items` (filters: `category, is_archived, needs_wash, search, page,
  page_size`), `GET /items/{id}`, **`PATCH /items/{id}`** (write-back),
  `POST /items/{id}/{wear|wash|archive|restore|analyze}`, `POST /items` (multipart
  create — UI only), `GET /items/{types,colors}`.
- Outfits: `GET /outfits`, `POST /outfits/suggest`, `GET /outfits/{id}`,
  `POST /outfits/{id}/{accept|reject|skip}`, `POST /outfits/{id}/feedback`.
- Notifications: `GET /notifications/history`, `POST /notifications/settings/{id}/test`.
- Images: **`GET /images/{user_id}/{filename}?expires=<ts>&sig=<hmac>`** → bytes.
  Item payloads carry `image_path`, `thumbnail_path`, `medium_path`; list responses
  include pre-signed `thumbnail_url` (cluster-internal).

`ItemUpdate` / `ItemTags` schema (the write-back target) is in
[`go-rewrite-spec.md`](go-rewrite-spec.md) §7.

## Bugs the rewrite should fix

1. `health` → use `/api/v1/health` (not `/health`).
2. Emit `WWW-Authenticate` on 401 (RFC 9728) → retires the Cloudflare Transform Rule.
3. Add `--external-email` so dev `/auth/sync` sends the real address → no email flap.

## Current live values (homelab, at time of writing)

- MCP `--external-id`: `your-user-id` (the real user; **27 items**).
- `AI_VISION_MODEL=qwen2.5vl:3b`, `AI_TEXT_MODEL=qwen2.5:1.5b` (only these two models
  remain on the Ollama PVC after cleanup).
- Hostnames: origin `wardrowbe-mcp.sitarski.tech`, portal `wardrowbe-portal.sitarski.tech`.
- Images: `ghcr.io/jansitarski/wardrowbe-*` (private; `ghcr-pull` secret).
- The k8s manifests, Cloudflare bring-up steps, and these decisions are documented in
  the `k8s-homelab` repo (`apps/wardrowbe*`, `build/README.md`).
