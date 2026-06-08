# Connecting Claude through Cloudflare Access

Claude's native connectors (Desktop / Mobile / web) require server-side OAuth, which a
static bearer key can't provide. To expose the bearer-protected `/mcp` endpoint to
them, front it with a Cloudflare tunnel and a Cloudflare Access MCP server portal.
This guide lists the configuration that makes the native connector work.

> Hostnames and team names below are placeholders (`example.com`,
> `<team>.cloudflareaccess.com`). Substitute your own.

## Topology

Expose the server through two hostnames on a `cloudflared` token-tunnel:

- **Origin** — `wardrowbe-mcp.example.com` → the MCP server `:8080` (bearer-protected).
- **Portal** — `wardrowbe-portal.example.com` → a Cloudflare Access MCP server portal
  that provides the OAuth the Connectors UI needs and injects the static bearer to the
  origin. Auth server: `https://<team>.cloudflareaccess.com`.

## Cloudflare configuration

Apply the following. Each item prevents a specific failure you would otherwise hit.

1. **WAF** — if you geo-restrict the domain, add a **Skip** rule for the `wardrowbe-*`
   hostnames, placed above the geo rule. Otherwise Cloudflare's MCP control plane and
   Anthropic's cloud (both out-of-region) are blocked with `403`.

2. **Tunnel** — point the public hostname at the in-cluster service
   (`...svc.cluster.local:8080`). A hostname typo surfaces as `502` (`no such host`).

3. **`WWW-Authenticate` on 401** — Claude discovers OAuth from this header. Run the MCP
   server with
   `--portal-resource-url https://<portal-host>/.well-known/oauth-protected-resource`
   so it emits the RFC 9728 header natively. If the portal strips it, add a Cloudflare
   **Transform Rule** that injects it on `401`.

4. **Redirect URIs** — in the Access OAuth app, add both
   `https://claude.ai/api/mcp/auth_callback` and
   `https://claude.com/api/mcp/auth_callback` to **Allowed redirect URLs** (the app
   defaults to `localhost` only). Access also requires the RFC 8707 `resource`
   parameter, which Claude sends automatically.

5. **Access policy** — `Allow` + `Include` the user's email. The default is deny, which
   surfaces as `403` at "Save and connect".

## Client options

| Client | How it connects |
|---|---|
| **Claude Code (CLI)** | Static bearer works directly: `claude mcp add --transport http <url> --header "Authorization: Bearer <key>"`. |
| **Desktop (config file)** | `mcp-remote` brokers the Access OAuth via a `localhost` redirect. |
| **Desktop / Mobile Connectors UI** | The portal plus the Cloudflare configuration above. |
