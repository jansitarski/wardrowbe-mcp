package wardrowbe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mimepkg "mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// apiBase is prefixed to every backend path.
const apiBase = "/api/v1"

// refreshLeeway is subtracted from the token TTL so we re-sync before expiry.
const refreshLeeway = 30 * time.Second

// maxAPIResponseBytes caps how much of a backend JSON response we buffer, so a
// misbehaving or compromised backend cannot exhaust memory.
const maxAPIResponseBytes = 16 << 20 // 16 MiB

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

// APIError represents a non-2xx backend response. Body holds the raw backend
// response for server-side debug logging only; Error() deliberately omits it so
// that backend-internal details (tokens, PII, stack traces) are never surfaced
// to the MCP caller / LLM. Log Body at debug where the error is handled.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("backend %s %s returned %d", e.Method, e.Path, e.StatusCode)
}

// readBoundedBody reads up to maxAPIResponseBytes from r, returning an error if
// the limit is exceeded so an oversized response fails loudly instead of OOMing.
func readBoundedBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAPIResponseBytes {
		return nil, fmt.Errorf("response exceeds %d MiB limit", maxAPIResponseBytes>>20)
	}
	return data, nil
}

// Client is the authenticated Wardrowbe backend HTTP client. It mirrors the
// Python WardrowbeClient: a JWT cache that re-syncs on expiry and retries once
// on a 401. Token refreshes are coalesced: one goroutine performs the network
// round trip while peers wait on a latch they can abandon when their own ctx
// is cancelled — the cache mutex is never held across a network call.
type Client struct {
	baseURL  string
	http     *http.Client
	provider TokenProvider
	log      *slog.Logger

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	inflight  *syncFlight // non-nil while a token sync is running
}

// syncFlight is one in-progress token refresh. token/err are written by the
// leader before done is closed; waiters read them only after <-done.
type syncFlight struct {
	done  chan struct{}
	token string
	err   error
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

// ensureToken returns a valid JWT, syncing if the cache is empty or near expiry.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	return c.tokenFor(ctx, "")
}

// tokenFor returns a cached valid token. badToken, when non-empty, is a token
// that just got a 401: if it still matches the cache the cache is stale and a
// re-sync is forced; if a peer already replaced it, the fresh cached token is
// returned without another /auth/sync (avoids a re-sync herd when many
// concurrent requests 401 at once). Refreshes are single-flight, and waiters
// honor their own ctx cancellation instead of blocking on a mutex.
func (c *Client) tokenFor(ctx context.Context, badToken string) (string, error) {
	c.mu.Lock()
	if c.token != "" && c.token != badToken && time.Now().Before(c.expiresAt) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}

	if fl := c.inflight; fl != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-fl.done:
		}
		return fl.token, fl.err
	}

	fl := &syncFlight{done: make(chan struct{})}
	c.inflight = fl
	c.token = ""
	c.mu.Unlock()

	token, expiresAt, err := c.doSync(ctx)

	c.mu.Lock()
	if err == nil {
		c.token = token
		c.expiresAt = expiresAt
	}
	c.inflight = nil
	c.mu.Unlock()

	fl.token, fl.err = token, err
	close(fl.done)
	return token, err
}

// doSync performs POST /auth/sync and returns the new token and its cache
// deadline. It must not touch the token cache — tokenFor owns that.
func (c *Client) doSync(ctx context.Context) (string, time.Time, error) {
	payload, err := c.provider.SyncPayload(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth sync: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth sync: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+apiBase+"/auth/sync", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth sync: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Debug("auth sync request failed", "err", err)
		return "", time.Time{}, fmt.Errorf("auth sync: request failed")
	}
	defer resp.Body.Close()
	raw, err := readBoundedBody(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth sync: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		c.log.Debug("auth sync failed", "status", resp.StatusCode, "body", string(raw))
		return "", time.Time{}, &APIError{StatusCode: resp.StatusCode, Method: http.MethodPost, Path: "/auth/sync", Body: string(raw)}
	}

	var sr syncResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", time.Time{}, fmt.Errorf("auth sync: decode response: %w", err)
	}
	if sr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("auth sync: empty access_token")
	}

	ttl := tokenTTL(sr)
	c.log.Debug("backend token synced", "external_id", payload.ExternalID, "ttl_s", int(ttl.Seconds()))
	return sr.AccessToken, time.Now().Add(ttl), nil
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

// apiRequest is one fully-prepared backend call. path is the relative API path
// used in logs/errors (never the absolute URL, which embeds the internal host);
// reqURL is the absolute URL actually dialed. A nil body with an empty
// contentType sends no body and no Content-Type header.
type apiRequest struct {
	method      string
	path        string
	reqURL      string
	contentType string
	body        []byte
}

// Request issues an authenticated JSON request to apiBase+path and returns the
// raw response body. On a 401 it re-syncs once and retries; a second 401 is an
// auth error. A 204 returns a nil body.
func (c *Client) Request(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	// Backstop against empty path segments: an empty id turns "/items/{id}"
	// into "/items/", which the backend redirects to the whole collection —
	// silently returning the wrong data. Tool handlers validate ids up front
	// (requireID); this catches any future call site that forgets.
	if strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return nil, fmt.Errorf("invalid api path %q (empty path segment)", path)
	}

	var encoded []byte
	contentType := ""
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		encoded = buf
		contentType = "application/json"
	}
	u := c.baseURL + apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return c.execute(ctx, apiRequest{method: method, path: path, reqURL: u, contentType: contentType, body: encoded})
}

// execute runs a prepared request with one 401-triggered token re-sync. A 204
// yields a nil body; any non-2xx yields an *APIError whose message omits the
// backend body so backend internals never reach the caller.
func (c *Client) execute(ctx context.Context, r apiRequest) (json.RawMessage, error) {
	raw, status, usedToken, err := c.attempt(ctx, r, "")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		c.log.Debug("backend 401, re-syncing token", "path", r.path)
		raw, status, _, err = c.attempt(ctx, r, usedToken)
		if err != nil {
			return nil, err
		}
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		c.log.Debug("backend error", "method", r.method, "path", r.path, "status", status, "body", string(raw))
		return nil, &APIError{StatusCode: status, Method: r.method, Path: r.path, Body: string(raw)}
	}
	return raw, nil
}

// attempt performs a single request and reports the bearer token it used.
// badToken, when non-empty, marks the token that just received a 401 so the
// token cache refreshes only if no peer already did (see tokenFor).
func (c *Client) attempt(ctx context.Context, r apiRequest, badToken string) (json.RawMessage, int, string, error) {
	token, err := c.tokenFor(ctx, badToken)
	if err != nil {
		return nil, 0, "", err
	}

	var reader io.Reader
	if r.body != nil {
		reader = bytes.NewReader(r.body)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, r.reqURL, reader)
	if err != nil {
		return nil, 0, token, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Don't surface raw transport errors (they embed internal hostnames/IPs)
		// to the MCP caller; log them and return a generic message.
		c.log.Debug("backend request failed", "method", r.method, "path", r.path, "err", err)
		return nil, 0, token, fmt.Errorf("backend %s %s: request failed", r.method, r.path)
	}
	defer resp.Body.Close()
	raw, err := readBoundedBody(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, token, fmt.Errorf("%s %s: read body: %w", r.method, r.path, err)
	}
	return raw, resp.StatusCode, token, nil
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
	// The MIME string can originate from a caller-supplied data URL and is
	// written verbatim into a multipart part header, which the multipart
	// package does not validate — a CR/LF inside it would inject extra
	// headers. Accept only a strict type/subtype token; otherwise sniff.
	if !validMIMEType.MatchString(mime) {
		mime = http.DetectContentType(data)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", mimepkg.FormatMediaType("form-data", map[string]string{
		"name":     "image",
		"filename": sanitizeFilename(filename),
	}))
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

// unsafeFilenameChars matches anything outside a conservative filename charset.
var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// validMIMEType matches a single conservative type/subtype token pair (RFC 2045
// tokens, no parameters), rejecting whitespace, control characters and anything
// that could smuggle a header line.
var validMIMEType = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)

// ValidMIMEType reports whether s is a single conservative type/subtype token
// pair (no parameters, no whitespace/control characters) — safe to embed in a
// multipart header. Shared with the MCP layer so caller-supplied MIME strings
// are screened by one rule everywhere.
func ValidMIMEType(s string) bool { return validMIMEType.MatchString(s) }

// sanitizeFilename reduces a (possibly user-supplied) filename to a safe charset
// and bounded length, preventing header/MIME-parameter injection in the upload.
func sanitizeFilename(name string) string {
	name = unsafeFilenameChars.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.TrimLeft(name, ".") // no leading dots / hidden-file tricks
	if name == "" {
		return "upload"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// requestRaw is the raw-body sibling of Request: it sends a pre-encoded body
// with an explicit Content-Type (e.g. multipart) and applies the same
// 401-resync-once retry via the shared execute path.
func (c *Client) requestRaw(ctx context.Context, method, path, contentType string, body []byte) (json.RawMessage, error) {
	return c.execute(ctx, apiRequest{
		method:      method,
		path:        path,
		reqURL:      c.baseURL + apiBase + path,
		contentType: contentType,
		body:        body,
	})
}

// DeleteOutfit permanently removes an outfit (DELETE /outfits/{id}). The backend
// replies 204, so a nil body indicates success.
func (c *Client) DeleteOutfit(ctx context.Context, outfitID string) (json.RawMessage, error) {
	return c.Request(ctx, http.MethodDelete, "/outfits/"+url.PathEscape(outfitID), nil, nil)
}

// Ping checks backend reachability for readiness probes. It hits GET /health
// and returns an error if the backend is unreachable or returns non-2xx.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Request(ctx, http.MethodGet, "/health", nil, nil)
	return err
}

// CoerceList normalizes a backend list response into a slice of raw messages.
// It accepts a bare array or an object wrapping the array under any of the known
// keys (items/results/data/outfits/notifications).
func CoerceList(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		// An empty body or JSON null (FastAPI often returns null for an empty
		// collection) is an empty list, not an error.
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
