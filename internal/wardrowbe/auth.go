package wardrowbe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// OIDCTokenProvider exchanges a refresh token for an id_token and projects its
// claims (sub, email, name) into a SyncPayload. Its methods use pointer receivers
// so the discovered token endpoint can be cached across calls.
type OIDCTokenProvider struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RefreshToken string
	// TokenEndpoint, when set, is used directly and discovery is skipped. Must
	// be https (validated in config). Useful for IdPs whose discovery is
	// unusual, or to pin the endpoint explicitly.
	TokenEndpoint string
	HTTPClient    *http.Client

	mu            sync.Mutex
	tokenEndpoint string // cached after first successful discovery
	refreshToken  string // current grant; replaces RefreshToken when the IdP rotates it
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

// SyncPayload implements TokenProvider by refreshing the id_token each call.
// The JWT cache in Client throttles how often this actually runs.
func (o *OIDCTokenProvider) SyncPayload(ctx context.Context) (SyncPayload, error) {
	tokenEndpoint, err := o.discoverTokenEndpoint(ctx)
	if err != nil {
		return SyncPayload{}, err
	}

	// Use the most recently issued refresh token: IdPs with rotation enabled
	// (Auth0, Okta, Keycloak with reuse detection) invalidate the old one on
	// every grant, so re-POSTing the original would fail the second refresh.
	o.mu.Lock()
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
		return SyncPayload{}, fmt.Errorf("oidc: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.client().Do(req)
	if err != nil {
		return SyncPayload{}, fmt.Errorf("oidc: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return SyncPayload{}, fmt.Errorf("oidc: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// RFC 6749 §5.2: failures arrive as a 400 with a JSON error body. Surface
		// error/error_description so e.g. an expired refresh token reads as
		// "invalid_grant" instead of a bare status code.
		var oerr oidcTokenResponse
		if json.Unmarshal(body, &oerr) == nil && oerr.Error != "" {
			if oerr.ErrDesc != "" {
				return SyncPayload{}, fmt.Errorf("oidc: token endpoint returned %d: %s (%s)", resp.StatusCode, oerr.Error, oerr.ErrDesc)
			}
			return SyncPayload{}, fmt.Errorf("oidc: token endpoint returned %d: %s", resp.StatusCode, oerr.Error)
		}
		return SyncPayload{}, fmt.Errorf("oidc: token endpoint returned %d", resp.StatusCode)
	}

	var tok oidcTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return SyncPayload{}, fmt.Errorf("oidc: decode token response: %w", err)
	}
	if tok.Error != "" {
		return SyncPayload{}, fmt.Errorf("oidc: token error %s", tok.Error)
	}
	if tok.IDToken == "" {
		return SyncPayload{}, fmt.Errorf("oidc: token response missing id_token")
	}
	if tok.RefreshToken != "" && tok.RefreshToken != refreshToken {
		// The IdP rotated the refresh token; the old one may now be invalid.
		o.mu.Lock()
		o.refreshToken = tok.RefreshToken
		o.mu.Unlock()
	}

	claims, err := decodeIDTokenClaims(tok.IDToken)
	if err != nil {
		return SyncPayload{}, err
	}
	if claims.Sub == "" {
		return SyncPayload{}, fmt.Errorf("oidc: id_token missing sub claim")
	}
	return SyncPayload{ExternalID: claims.Sub, Email: claims.Email, DisplayName: claims.Name}, nil
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
// signature. The id_token was just retrieved over TLS directly from the issuer's
// token endpoint, so transport already authenticates it; the backend re-validates
// the projected identity on /auth/sync.
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
