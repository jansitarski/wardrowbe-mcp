package wardrowbe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// apiBase is prefixed to every backend path.
const apiBase = "/api/v1"

// refreshLeeway is subtracted from the token TTL so we re-sync before expiry.
const refreshLeeway = 30 * time.Second

// Token-cache bounds. The dev backend returns expires_in:null while the JWT
// itself carries a multi-day exp; without these we would re-sync on every
// request and overflow the /auth/sync rate limit.
const (
	// defaultTokenTTL is used when neither expires_in nor a JWT exp is available.
	defaultTokenTTL = 30 * time.Minute
	// minTokenTTL floors the cache window so a missing/near/expired claim can
	// never degrade into a per-request re-sync storm.
	minTokenTTL = 60 * time.Second
)

// APIError represents a non-2xx backend response.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("backend %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, body)
}

// Client is the authenticated Wardrowbe backend HTTP client. It mirrors the
// Python WardrowbeClient: a mutex-guarded JWT cache that re-syncs on expiry and
// retries once on a 401.
type Client struct {
	baseURL  string
	http     *http.Client
	provider TokenProvider
	log      *slog.Logger

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewClient builds a Client. httpClient must have sane timeouts configured by
// the caller; if nil a default with a 60s timeout is used.
func NewClient(baseURL string, provider TokenProvider, httpClient *http.Client, log *slog.Logger) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		http:     httpClient,
		provider: provider,
		log:      log,
	}
}

// token returns a valid JWT, syncing if the cache is empty or near expiry.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiresAt) {
		return c.token, nil
	}
	return c.syncLocked(ctx)
}

// forceSync clears the cached token and syncs again (used after a 401).
func (c *Client) forceSync(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	return c.syncLocked(ctx)
}

// syncLocked performs POST /auth/sync. Caller must hold c.mu.
func (c *Client) syncLocked(ctx context.Context) (string, error) {
	payload, err := c.provider.SyncPayload(ctx)
	if err != nil {
		return "", fmt.Errorf("auth sync: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("auth sync: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+apiBase+"/auth/sync", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("auth sync: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth sync: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{StatusCode: resp.StatusCode, Method: http.MethodPost, Path: "/auth/sync", Body: string(raw)}
	}

	var sr syncResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", fmt.Errorf("auth sync: decode response: %w", err)
	}
	if sr.AccessToken == "" {
		return "", fmt.Errorf("auth sync: empty access_token")
	}

	ttl := tokenTTL(sr)
	c.token = sr.AccessToken
	c.expiresAt = time.Now().Add(ttl)
	c.log.Debug("backend token synced", "external_id", payload.ExternalID, "ttl_s", int(ttl.Seconds()))
	return c.token, nil
}

// tokenTTL decides how long to cache an access token. It prefers expires_in,
// falls back to the JWT's exp claim (the dev backend reports expires_in:null but
// signs a long-lived token), then a default — always clamped to >= minTokenTTL
// so we cannot re-sync on every request.
func tokenTTL(sr syncResponse) time.Duration {
	var ttl time.Duration
	switch {
	case sr.ExpiresIn > 0:
		ttl = time.Duration(sr.ExpiresIn) * time.Second
	default:
		if exp, ok := jwtExpiry(sr.AccessToken); ok {
			ttl = time.Until(exp)
		} else {
			ttl = defaultTokenTTL
		}
	}
	ttl -= refreshLeeway
	if ttl < minTokenTTL {
		ttl = minTokenTTL
	}
	return ttl
}

// jwtExpiry reads the exp claim from a JWT without verifying its signature
// (the token was just received over TLS from the backend). Returns false if the
// token is malformed or carries no exp.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// Request issues an authenticated JSON request to apiBase+path and returns the
// raw response body. On a 401 it re-syncs once and retries; a second 401 is an
// auth error. A 204 returns a nil body.
func (c *Client) Request(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	raw, status, err := c.do(ctx, method, path, query, body, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		c.log.Debug("backend 401, re-syncing token", "path", path)
		raw, status, err = c.do(ctx, method, path, query, body, true)
		if err != nil {
			return nil, err
		}
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Method: method, Path: path, Body: string(raw)}
	}
	return raw, nil
}

// do performs a single attempt. When forceResync is true a fresh token is
// fetched first (used on retry after a 401).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, forceResync bool) (json.RawMessage, int, error) {
	var token string
	var err error
	if forceResync {
		token, err = c.forceSync(ctx)
	} else {
		token, err = c.ensureToken(ctx)
	}
	if err != nil {
		return nil, 0, err
	}

	var reader io.Reader
	if body != nil {
		buf, mErr := json.Marshal(body)
		if mErr != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", mErr)
		}
		reader = bytes.NewReader(buf)
	}

	u := c.baseURL + apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return raw, resp.StatusCode, nil
}

// UpdateItem PATCHes an item and returns the raw updated item payload.
func (c *Client) UpdateItem(ctx context.Context, itemID string, patch ItemUpdate) (json.RawMessage, error) {
	return c.Request(ctx, http.MethodPatch, "/items/"+url.PathEscape(itemID), nil, patch)
}

// CreateStudioOutfit persists a manually composed outfit (POST /outfits/studio)
// and returns the raw created outfit payload.
func (c *Client) CreateStudioOutfit(ctx context.Context, outfit StudioOutfit) (json.RawMessage, error) {
	return c.Request(ctx, http.MethodPost, "/outfits/studio", nil, outfit)
}

// CreateItemFromImage uploads image bytes plus optional metadata as
// multipart/form-data to POST /items and returns the raw created item. The
// backend stores the image and queues AI auto-tagging.
func (c *Client) CreateItemFromImage(ctx context.Context, data []byte, filename, mime string, fields map[string]string) (json.RawMessage, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename=%q`, filename))
	hdr.Set("Content-Type", mime)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, fmt.Errorf("build multipart: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write image part: %w", err)
	}
	for k, v := range fields {
		if v == "" {
			continue
		}
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("write field %q: %w", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	return c.requestRaw(ctx, http.MethodPost, "/items", mw.FormDataContentType(), buf.Bytes())
}

// requestRaw is the raw-body sibling of Request: it sends a pre-encoded body
// with an explicit Content-Type and applies the same 401-resync-once retry.
func (c *Client) requestRaw(ctx context.Context, method, path, contentType string, body []byte) (json.RawMessage, error) {
	raw, status, err := c.doRaw(ctx, method, path, contentType, body, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		c.log.Debug("backend 401, re-syncing token", "path", path)
		raw, status, err = c.doRaw(ctx, method, path, contentType, body, true)
		if err != nil {
			return nil, err
		}
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Method: method, Path: path, Body: string(raw)}
	}
	return raw, nil
}

func (c *Client) doRaw(ctx context.Context, method, path, contentType string, body []byte, forceResync bool) (json.RawMessage, int, error) {
	var token string
	var err error
	if forceResync {
		token, err = c.forceSync(ctx)
	} else {
		token, err = c.ensureToken(ctx)
	}
	if err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+apiBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return raw, resp.StatusCode, nil
}

// DeleteOutfit permanently removes an outfit (DELETE /outfits/{id}). The backend
// replies 204, so a nil body indicates success.
func (c *Client) DeleteOutfit(ctx context.Context, outfitID string) (json.RawMessage, error) {
	return c.Request(ctx, http.MethodDelete, "/outfits/"+url.PathEscape(outfitID), nil, nil)
}

// CoerceList normalizes a backend list response into a slice of raw messages.
// It accepts a bare array or an object wrapping the array under any of the known
// keys (items/results/data/outfits/notifications).
func CoerceList(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("coerce list: %w", err)
		}
		return arr, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, fmt.Errorf("coerce list: %w", err)
	}
	for _, key := range []string{"items", "results", "data", "outfits", "notifications"} {
		if v, ok := obj[key]; ok {
			var arr []json.RawMessage
			if err := json.Unmarshal(v, &arr); err != nil {
				return nil, fmt.Errorf("coerce list key %q: %w", key, err)
			}
			return arr, nil
		}
	}
	// Single object — wrap it so callers can treat everything uniformly.
	return []json.RawMessage{trimmed}, nil
}
