package wardrowbe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// oidcHTTPTimeout bounds OIDC discovery/token requests when no client is injected.
const oidcHTTPTimeout = 30 * time.Second

// TokenProvider yields the identity payload sent to /auth/sync. The dev provider
// returns static values; the OIDC provider refreshes an id_token and projects
// its claims.
type TokenProvider interface {
	SyncPayload(ctx context.Context) (SyncPayload, error)
}

// DevTokenProvider returns a fixed identity. Email defaults to
// <external-id>@wardrowbe.local when empty (handled in config).
type DevTokenProvider struct {
	ExternalID  string
	Email       string
	DisplayName string
}

// SyncPayload implements TokenProvider.
func (d DevTokenProvider) SyncPayload(_ context.Context) (SyncPayload, error) {
	if d.ExternalID == "" {
		return SyncPayload{}, fmt.Errorf("dev token provider: external id is empty")
	}
	display := d.DisplayName
	if display == "" {
		display = d.ExternalID
	}
	return SyncPayload{ExternalID: d.ExternalID, Email: d.Email, DisplayName: display}, nil
}

// OIDCTokenProvider yields an id_token and projects its claims (sub, email,
// name) into a SyncPayload, forwarding the raw id_token so the backend can
// validate it against the issuer's JWKS. Its methods use pointer receivers so
// the discovered token endpoint (and any rotated refresh token) can be cached
// across calls.
//
// The id_token is obtained one of two ways:
//   - RefreshToken set: run the refresh_token grant against the issuer's token
//     endpoint on every sync, so each call gets a freshly-minted id_token. This
//     is the durable path for long-running servers.
//   - RefreshToken empty, IDToken set: use the static IDToken as-is. Simpler to
//     configure, but the token expires and is not renewed — for issuers that do
//     not support the refresh_token grant, accepting that the connection must be
//     reconfigured when the token lapses.
//
// When both are set the refresh token wins (the durable path); the static
// IDToken is then unused.
type OIDCTokenProvider struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RefreshToken string
	// IDToken is a static id_token used when no RefreshToken is configured.
	IDToken string
	// RefreshTokenFile, when set, is where the current refresh token is persisted
	// and reloaded across restarts. IdPs that rotate refresh tokens single-use
	// (e.g. Cloudflare Access) otherwise invalidate the configured seed after the
	// first refresh, so a restart would fall back to a dead token. With this set,
	// each rotation is written here and the latest is loaded on startup.
	RefreshTokenFile string
	// TokenEndpoint, when set, is used directly and discovery is skipped. Must
	// be https (validated in config). Useful for IdPs whose discovery is
	// unusual, or to pin the endpoint explicitly.
	TokenEndpoint string
	HTTPClient    *http.Client

	mu            sync.Mutex
	tokenEndpoint string // cached after first successful discovery
	refreshToken  string // current grant; replaces RefreshToken when the IdP rotates it
	loadedFile    bool   // whether RefreshTokenFile has been read once
}

type oidcDiscovery struct {
	TokenEndpoint string `json:"token_endpoint"`
}

type oidcTokenResponse struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrDesc      string `json:"error_description"`
}

type idTokenClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// SyncPayload implements TokenProvider. It obtains a current id_token (via the
// refresh_token grant when a refresh token is configured, otherwise the static
// id_token), projects its claims, and forwards the raw token so the backend can
// validate it. The JWT cache in Client throttles how often this actually runs.
func (o *OIDCTokenProvider) SyncPayload(ctx context.Context) (SyncPayload, error) {
	idToken, err := o.idToken(ctx)
	if err != nil {
		return SyncPayload{}, err
	}

	claims, err := decodeIDTokenClaims(idToken)
	if err != nil {
		return SyncPayload{}, err
	}
	if strings.TrimSpace(claims.Sub) == "" {
		return SyncPayload{}, fmt.Errorf("oidc: id_token missing sub claim")
	}
	// The backend requires a non-empty display_name, but the `name` claim is
	// optional and some issuers omit it (e.g. Cloudflare Access id_tokens minted
	// via the refresh_token grant carry only `sub`). Fall back to email, then
	// sub (non-blank per the check above), mirroring DevTokenProvider. Trim each
	// candidate so a whitespace-only claim doesn't slip through as a blank name.
	display := strings.TrimSpace(claims.Name)
	if display == "" {
		display = strings.TrimSpace(claims.Email)
	}
	if display == "" {
		display = claims.Sub
	}
	return SyncPayload{
		ExternalID:  claims.Sub,
		Email:       claims.Email,
		DisplayName: display,
		IDToken:     idToken,
	}, nil
}

// idToken returns a current id_token. With a refresh token configured it runs
// the refresh_token grant (the Client's JWT cache bounds the frequency);
// otherwise it returns the statically-configured id_token as-is.
func (o *OIDCTokenProvider) idToken(ctx context.Context) (string, error) {
	if o.RefreshToken == "" {
		if o.IDToken == "" {
			return "", fmt.Errorf("oidc: no refresh token or id_token configured")
		}
		// The static token is never renewed. Fail fast with an actionable message
		// if it has already expired, rather than forwarding a token the backend
		// will reject — which, with no cached access token, would re-sync against
		// /auth/sync on every request with no backoff.
		if exp, ok := jwtExpiry(o.IDToken); ok && time.Now().After(exp) {
			return "", fmt.Errorf("oidc: configured id_token expired at %s; mint a fresh --oidc-id-token "+
				"(MCP_OIDC_ID_TOKEN) or switch to --oidc-refresh-token", exp.UTC().Format(time.RFC3339))
		}
		return o.IDToken, nil
	}
	return o.refreshIDToken(ctx)
}

// refreshIDToken exchanges the configured (or most recently rotated) refresh
// token for a fresh id_token at the issuer's discovered token endpoint.
func (o *OIDCTokenProvider) refreshIDToken(ctx context.Context) (string, error) {
	tokenEndpoint, err := o.discoverTokenEndpoint(ctx)
	if err != nil {
		return "", err
	}

	// Use the most recently issued refresh token: IdPs with rotation enabled
	// (Auth0, Okta, Keycloak with reuse detection) invalidate the old one on
	// every grant, so re-POSTing the original would fail the second refresh.
	// On the first call, seed the in-memory token from RefreshTokenFile (the
	// token persisted by a previous process) so a restart resumes from the last
	// rotated token rather than the now-dead configured seed.
	o.mu.Lock()
	if o.RefreshTokenFile != "" && !o.loadedFile {
		o.loadedFile = true
		if data, err := os.ReadFile(o.RefreshTokenFile); err == nil {
			if t := strings.TrimSpace(string(data)); t != "" {
				o.refreshToken = t
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("oidc: could not read refresh token file; falling back to configured seed",
				"file", o.RefreshTokenFile, "err", err)
		}
	}
	refreshToken := o.refreshToken
	o.mu.Unlock()
	if refreshToken == "" {
		refreshToken = o.RefreshToken
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {o.ClientID},
	}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oidc: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("oidc: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// RFC 6749 §5.2: failures arrive as a 400 with a JSON error body. Surface
		// error/error_description so e.g. an expired refresh token reads as
		// "invalid_grant" instead of a bare status code.
		var oerr oidcTokenResponse
		if json.Unmarshal(body, &oerr) == nil && oerr.Error != "" {
			if oerr.ErrDesc != "" {
				return "", fmt.Errorf("oidc: token endpoint returned %d: %s (%s)", resp.StatusCode, oerr.Error, oerr.ErrDesc)
			}
			return "", fmt.Errorf("oidc: token endpoint returned %d: %s", resp.StatusCode, oerr.Error)
		}
		return "", fmt.Errorf("oidc: token endpoint returned %d", resp.StatusCode)
	}

	var tok oidcTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("oidc: decode token response: %w", err)
	}
	if tok.Error != "" {
		return "", fmt.Errorf("oidc: token error %s", tok.Error)
	}
	if tok.IDToken == "" {
		return "", fmt.Errorf("oidc: token response missing id_token")
	}
	if tok.RefreshToken != "" && tok.RefreshToken != refreshToken {
		// The IdP rotated the refresh token; the old one may now be invalid.
		o.mu.Lock()
		firstRotation := o.refreshToken == ""
		o.refreshToken = tok.RefreshToken
		o.mu.Unlock()
		if o.RefreshTokenFile != "" {
			// Persist the rotation so a restart resumes from it instead of the
			// dead seed. Best-effort: on failure we still hold it in memory and
			// keep serving until the next restart.
			if err := persistRefreshToken(o.RefreshTokenFile, tok.RefreshToken); err != nil {
				slog.Warn("oidc: failed to persist rotated refresh token; it survives only in memory until restart",
					"file", o.RefreshTokenFile, "err", err)
			}
		} else if firstRotation {
			// The rotated token lives only in process memory: after a restart the
			// server resumes from the configured seed token, which a
			// rotation-enforcing IdP has already invalidated (reuse detection may
			// even revoke the whole token family). Warn once so the operator knows
			// to keep the stored token fresh — or set --oidc-refresh-token-file.
			slog.Warn("oidc: idp rotated the refresh token; the rotation is held in memory only — " +
				"after a restart the server resumes from the configured refresh token, which the idp " +
				"may now reject (invalid_grant). Set --oidc-refresh-token-file (MCP_OIDC_REFRESH_TOKEN_FILE) " +
				"to persist rotations across restarts, or re-mint and update MCP_OIDC_REFRESH_TOKEN.")
		}
	}
	return tok.IDToken, nil
}

// persistRefreshToken atomically writes the current refresh token to path
// (0600), via a temp file + rename so a crash mid-write can't truncate it.
func persistRefreshToken(path, token string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".oidc-refresh-token-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if the rename succeeded
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (o *OIDCTokenProvider) discoverTokenEndpoint(ctx context.Context) (string, error) {
	// An explicit override skips discovery entirely.
	if o.TokenEndpoint != "" {
		return o.TokenEndpoint, nil
	}

	// The token endpoint never changes for a given issuer; cache it after the
	// first successful discovery so each token refresh doesn't re-fetch the
	// discovery document.
	o.mu.Lock()
	cached := o.tokenEndpoint
	o.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	discoveryURL := strings.TrimRight(o.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("oidc: build discovery request: %w", err)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc: discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: discovery returned %d", resp.StatusCode)
	}
	var disc oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&disc); err != nil {
		return "", fmt.Errorf("oidc: decode discovery: %w", err)
	}
	if disc.TokenEndpoint == "" {
		return "", fmt.Errorf("oidc: discovery missing token_endpoint")
	}
	// The client secret and refresh token are POSTed to this endpoint, so it
	// must be https. We deliberately do NOT pin it to the issuer's host: major
	// IdPs serve the token endpoint from a different domain (Google:
	// accounts.google.com -> oauth2.googleapis.com; AWS Cognito similarly), and
	// the discovery document was just fetched over TLS from the config-validated
	// https issuer — the transport already authenticates it. Operators who want
	// a pin can set TokenEndpoint explicitly.
	ep, err := url.Parse(disc.TokenEndpoint)
	if err != nil || ep.Scheme != "https" || ep.Host == "" {
		return "", fmt.Errorf("oidc: token_endpoint is not a valid https URL")
	}
	// A cross-host endpoint is where the refresh token and client secret will be
	// POSTed, so make the expanded trust visible: anyone who can influence the
	// discovery document chooses that destination. Operators who want it pinned
	// set --oidc-token-endpoint.
	if iss, perr := url.Parse(o.Issuer); perr == nil && !strings.EqualFold(ep.Host, iss.Host) {
		slog.Warn("oidc: discovered token_endpoint is on a different host than the issuer "+
			"(normal for e.g. Google and AWS Cognito); set --oidc-token-endpoint to pin it explicitly",
			"issuer_host", iss.Host, "token_endpoint_host", ep.Host)
	}

	o.mu.Lock()
	o.tokenEndpoint = disc.TokenEndpoint
	o.mu.Unlock()
	return disc.TokenEndpoint, nil
}

func (o *OIDCTokenProvider) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	// Never fall back to http.DefaultClient — it has no timeout.
	return &http.Client{Timeout: oidcHTTPTimeout}
}

// decodeIDTokenClaims extracts the payload of a JWT without verifying its
// signature. Local decoding only projects the identity for the sync body's
// convenience fields; the raw id_token is forwarded alongside it so the backend
// performs the authoritative signature/issuer validation on /auth/sync.
func decodeIDTokenClaims(idToken string) (idTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return idTokenClaims{}, fmt.Errorf("oidc: malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idTokenClaims{}, fmt.Errorf("oidc: decode id_token payload: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return idTokenClaims{}, fmt.Errorf("oidc: decode id_token claims: %w", err)
	}
	return claims, nil
}
