# Security Policy

## Reporting a vulnerability

Please report security issues privately via GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
("Report a vulnerability" under the **Security** tab), rather than opening a
public issue. Please include a description, reproduction steps, and the impact
you observed. You can expect an initial response within a few days.

## Scope and threat model

`wardrowbe-mcp` is a thin MCP gateway in front of a Wardrowbe backend. Notable
security properties:

- **Incoming auth.** The HTTP transport requires a static bearer key
  (`MCP_API_KEY`); an empty key is rejected at startup, and the comparison is
  constant-time. On `401` the server emits an RFC 9728 `WWW-Authenticate`
  header so OAuth-capable clients can discover the protected-resource metadata.
- **Outgoing image fetch.** `wardrowbe_create_item_from_url` fetches remote images behind
  an SSRF guard: only `http(s)` URLs are allowed (re-checked on every redirect
  hop), and the IP actually dialed is refused if it is
  loopback/private/link-local/CGNAT/multicast/NAT64/unspecified. Because the
  check runs on the resolved IP at dial time — not on the hostname — it closes
  the classic client-side DNS-rebinding window. The response is size-capped,
  redirect-count-capped, and content-type-validated. Note this guards the
  *transport*: it does not interpret application-layer redirects a cooperating
  server might encode in a 200 body, and the backend that ultimately stores the
  image is trusted.
- **Backend-supplied image URLs are trusted.** `wardrowbe_get_item_image` /
  `wardrowbe_get_outfit_images` fetch whatever URL the *backend* reports for a
  stored garment (typically a relative path on the backend itself, sometimes a
  pre-signed absolute URL). These fetches deliberately do **not** go through
  the SSRF guard: the backend is inside the trust boundary and its storage may
  legitimately live on private addresses (MinIO, NAS). Only *caller*-supplied
  URLs (`wardrowbe_create_item_from_url`) get the SSRF treatment. If your
  backend is not trustworthy, this server is the wrong deployment model.
- **Reference-based image download is host- and path-scoped.**
  `wardrowbe_download_image` fetches an image by a reference the caller passes
  (a relative `/api/v1/images/...` path or a full URL), over the *authenticated*
  backend connection — which is what lets it retrieve images when the backend
  sits behind a Cloudflare Access (or similar) tunnel that bounces an
  unauthenticated direct URL fetch to a login page. Because the fetch carries the
  bearer token, the reference is validated before dialing: the host (for an
  absolute URL) must equal the configured backend host, and the resolved path
  must live under `/api/v1/images/`. This keeps the tool an image fetcher rather
  than an authenticated proxy to arbitrary hosts or other backend endpoints; an
  out-of-scope reference is rejected with a clear, internals-free message.
- **Secrets.** All credentials (API key, OIDC client secret / refresh token)
  come from flags or environment variables. None are committed to the repo.

## Supported versions

This is a personal project; only the latest release on `master` receives fixes.
