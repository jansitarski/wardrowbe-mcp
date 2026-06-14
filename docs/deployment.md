# Deploying wardrowbe-mcp

How to run wardrowbe-mcp against a [Wardrowbe](https://github.com/Anyesh/wardrowbe)
backend and connect it to Claude.

> Hostnames, emails, and IPs in this guide are placeholders (`example.com`,
> `you@example.com`, `192.0.2.0/24`). Substitute your own.

## Overview

wardrowbe-mcp sits in front of a Wardrowbe deployment — Postgres + Redis + a FastAPI
backend + an arq worker + a Next.js frontend — and exposes the backend API to Claude
as MCP tools. Run one MCP server per backend; Claude connects to it.

Claude can also **add items** through the server: `wardrowbe_create_item_from_url`
fetches a public image URL and `wardrowbe_create_item_from_base64` takes an inline image, both uploading
to the backend (which stores and auto-tags the garment) — so a wardrobe can be built
or extended directly from chat, not just the web UI.

Wardrowbe requires an AI backend for garment auto-tagging and outfit suggestions.
Any OpenAI-compatible endpoint works, and a small local model (e.g. Ollama) is enough:
the server's image-view and write-back tools let Claude do the accurate tagging
itself, so the local model only has to satisfy Wardrowbe's upload-time auto-tag. See
[AI backend](#ai-backend).

## Reference topology

A typical k3s deployment wires the services together over the cluster network:

| Component | Address (example) | Notes |
|---|---|---|
| Backend (FastAPI) | `backend.wardrowbe.svc.cluster.local:8000` | API under `/api/v1` |
| MCP server | `wardrowbe-mcp.wardrowbe.svc.cluster.local:8080` | bearer-protected `/mcp` |
| Frontend (Next.js) | `frontend.wardrowbe.svc.cluster.local:3000` | NextAuth at `/api/auth/*` |
| AI endpoint (Ollama) | `ollama.ollama.svc.cluster.local:11434` | OpenAI-compatible `/v1` |
| Web UI ingress | `http://wardrowbe.local` | internal-only nginx |

Route the ingress carefully: `/api/auth` → frontend, `/api` → backend, `/` → frontend.
Sending all `/api` to the backend breaks NextAuth login (FastAPI returns
`{"detail":"Not Found"}`). Allow large upload bodies (`proxy-body-size: 50m`) and a
long read/send timeout (`300s`) so slow AI suggestions don't time out.

## Backend auth (dev mode)

The MCP server authenticates to the backend via `POST /api/v1/auth/sync`, which the
backend exposes in dev mode. Set on the backend:

- `DEBUG=true`
- `SECRET_KEY=<a strong random secret — generate with: openssl rand -hex 32>`
  (this signs backend JWTs; never use a placeholder/default value, even in dev)

For the web login form to appear, also set `DEV_MODE=true` on the **frontend** — this
is separate from the backend `DEBUG` flag.

## Identity (`--external-id` / `--external-email`)

The MCP server and the web UI must resolve to the **same user** so they share one
wardrobe. Wardrowbe keys users by `external_id`:

- The web login derives `external_id` from the email by replacing `@` and `.` with `-`
  (e.g. `you@example.com` → `you-example-com`). Set `--external-id` to that exact value.
- Set `--external-email` to the real email. Without it the server derives
  `<external-id>@wardrowbe.local`, and the stored account email flips between that and
  the real address depending on which side synced last. Items key on `user_id`, so this
  is cosmetic — but set it to keep the email stable.

For a real identity provider, use `--auth oidc` with `--oidc-issuer-url` and
`--oidc-client-id`, plus a token source:

- `--oidc-refresh-token` (durable): the server runs the `refresh_token` grant to
  mint a fresh `id_token` on every sync. Requires an issuer that supports the
  grant — e.g. for a Cloudflare Access SaaS app, enable refresh tokens on the app
  and request the `offline_access` scope during the initial authorization.
- `--oidc-id-token` (fallback): a static `id_token`, used as-is when the issuer
  does not issue refresh tokens. It expires and is not renewed, so the connection
  must be reconfigured when it lapses.

Either way the raw `id_token` is forwarded in the `/auth/sync` body — the backend
validates it against the issuer's JWKS and reads `sub` / `email` / `name` from it.

## AI backend

Point Wardrowbe at an OpenAI-compatible endpoint with `AI_BASE_URL`, `AI_VISION_MODEL`,
and `AI_TEXT_MODEL`. Choosing models:

- **Vision** — a ~3B model (e.g. `qwen2.5vl:3b`) runs CPU-only in about 8 GiB and is
  adequate as a rough-draft tagger; 7B vision models typically need ~10–11 GiB. Fine
  attributes like fabric (linen vs cotton) are unreliable from a photo at any size.
- **Text** — only needs to emit valid JSON for the recommender, so a tiny model (e.g.
  `qwen2.5:1.5b`) is fine.

Don't over-invest in the local model. Use `wardrowbe_get_item_image` together with
`wardrowbe_set_item_tags` / `wardrowbe_set_item_description` to have Claude tag items
accurately on demand; the local model only needs to handle the upload-time auto-tag.

## Connecting Claude

- **Local** — run with `--transport stdio` (for a Claude Desktop config-file entry), or
  with `--transport http` plus the `MCP_API_KEY` env var and register it:

  ```bash
  claude mcp add --transport http wardrowbe <url> --header "Authorization: Bearer $MCP_API_KEY"
  ```

- **Public, native connectors** — to use the Desktop / Mobile / web Connectors UI, front
  the server with a Cloudflare tunnel and Access portal: see
  [connecting-claude-via-cloudflare.md](connecting-claude-via-cloudflare.md).
