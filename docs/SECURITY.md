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
- **Outgoing image fetch.** `create_item_from_url` fetches remote images behind
  an SSRF guard: only `http(s)` URLs are allowed, the dialed IP is re-checked
  and refused if it is loopback/private/link-local/CGNAT/unspecified (which
  also defeats DNS rebinding), and the response is size-capped and
  content-type-validated.
- **Secrets.** All credentials (API key, OIDC client secret / refresh token)
  come from flags or environment variables. None are committed to the repo.

## Supported versions

This is a personal project; only the latest release on `master` receives fixes.
