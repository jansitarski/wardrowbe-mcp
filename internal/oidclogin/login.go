// Package oidclogin implements a one-shot, interactive OIDC Authorization Code
// + PKCE login over a loopback redirect (RFC 8252). It is used by the
// `wardrowbe-mcp login` command to mint a refresh token without copy-pasting an
// authorization code: it opens the browser, captures the callback on a local
// listener, exchanges the code, and returns the tokens.
//
// This is deliberately generic — it works against any OIDC issuer (endpoints
// come from discovery, or can be pinned) and any client (public or confidential,
// via an optional client secret). Nothing here is wardrowbe- or Cloudflare-
// specific. The headless MCP server itself can never run this flow (no browser);
// it is purely an operator convenience for obtaining the long-lived credential
// the server then uses.
package oidclogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

// Options configures a login run.
type Options struct {
	Issuer        string   // OIDC issuer (used for discovery unless endpoints are pinned)
	ClientID      string   // OAuth client id (required)
	ClientSecret  string   // optional; sent on the token request for confidential clients
	AuthEndpoint  string   // optional override; skips discovery for the authorization endpoint
	TokenEndpoint string   // optional override; skips discovery for the token endpoint
	RedirectURL   string   // loopback redirect, e.g. http://127.0.0.1:8976/callback (must be registered at the IdP)
	Scopes        []string // e.g. openid, email, profile, offline_access
	OpenBrowser   bool     // attempt to open the system browser at the authorize URL
	HTTPClient    *http.Client
}

// Result holds what the token endpoint returned.
type Result struct {
	RefreshToken string
	IDToken      string
	AccessToken  string
	ExpiresIn    int
	Subject      string // id_token `sub`, if present (informational)
	Email        string // id_token `email`, if present (informational)
}

// Run drives the full flow: resolve endpoints, build the authorize URL, capture
// the loopback callback, and exchange the code. Human-facing progress is written
// to `out` (typically stderr); the returned Result carries the tokens.
func Run(ctx context.Context, opts Options, out io.Writer) (Result, error) {
	if opts.ClientID == "" {
		return Result{}, fmt.Errorf("login: client id is required")
	}
	if opts.RedirectURL == "" {
		return Result{}, fmt.Errorf("login: redirect URL is required")
	}
	redirect, err := parseLoopbackRedirect(opts.RedirectURL)
	if err != nil {
		return Result{}, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	authEP, tokenEP, err := resolveEndpoints(ctx, client, opts)
	if err != nil {
		return Result{}, err
	}

	verifier, challenge, err := newPKCE()
	if err != nil {
		return Result{}, err
	}
	state, err := randomURLToken()
	if err != nil {
		return Result{}, err
	}

	authURL := buildAuthURL(authEP, opts.ClientID, opts.RedirectURL, opts.Scopes, state, challenge)

	fmt.Fprintf(out, "Open this URL in a browser logged in to your IdP:\n\n  %s\n\n", authURL)
	if opts.OpenBrowser {
		if err := openBrowser(authURL); err != nil {
			fmt.Fprintf(out, "(could not open a browser automatically: %v — open the URL above manually)\n", err)
		}
	}
	fmt.Fprintf(out, "Waiting for the callback on %s ...\n", opts.RedirectURL)

	code, err := awaitCallback(ctx, redirect, state)
	if err != nil {
		return Result{}, err
	}

	res, err := exchangeCode(ctx, client, tokenEP, opts, code, verifier, opts.RedirectURL)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

type loopbackRedirect struct {
	addr string // host:port to listen on
	path string // callback path, e.g. /callback
}

// parseLoopbackRedirect validates that the redirect targets a loopback address
// (RFC 8252) so the local listener never binds a public interface.
func parseLoopbackRedirect(raw string) (loopbackRedirect, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return loopbackRedirect{}, fmt.Errorf("login: redirect URL must be an http loopback URL like http://127.0.0.1:8976/callback")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return loopbackRedirect{}, fmt.Errorf("login: redirect host %q is not loopback (use 127.0.0.1, ::1, or localhost)", host)
		}
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return loopbackRedirect{addr: u.Host, path: path}, nil
}

type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// resolveEndpoints returns the authorization and token endpoints, preferring
// explicit overrides and otherwise fetching the issuer's discovery document.
func resolveEndpoints(ctx context.Context, client *http.Client, opts Options) (authEP, tokenEP string, err error) {
	authEP, tokenEP = opts.AuthEndpoint, opts.TokenEndpoint
	if authEP == "" || tokenEP == "" {
		if opts.Issuer == "" {
			return "", "", fmt.Errorf("login: issuer URL is required unless both --oidc-auth-endpoint and --oidc-token-endpoint are set")
		}
		disc, derr := fetchDiscovery(ctx, client, opts.Issuer)
		if derr != nil {
			return "", "", derr
		}
		if authEP == "" {
			authEP = disc.AuthorizationEndpoint
		}
		if tokenEP == "" {
			tokenEP = disc.TokenEndpoint
		}
	}
	if err := requireHTTPS(authEP, "authorization_endpoint"); err != nil {
		return "", "", err
	}
	if err := requireHTTPS(tokenEP, "token_endpoint"); err != nil {
		return "", "", err
	}
	return authEP, tokenEP, nil
}

func fetchDiscovery(ctx context.Context, client *http.Client, issuer string) (oidcDiscovery, error) {
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("login: build discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("login: discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("login: discovery returned %d", resp.StatusCode)
	}
	var disc oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&disc); err != nil {
		return oidcDiscovery{}, fmt.Errorf("login: decode discovery: %w", err)
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" {
		return oidcDiscovery{}, fmt.Errorf("login: discovery missing authorization_endpoint or token_endpoint")
	}
	return disc, nil
}

func requireHTTPS(raw, what string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("login: %s is not a valid https URL (%q)", what, raw)
	}
	return nil
}

// newPKCE returns a high-entropy code_verifier and its S256 code_challenge.
func newPKCE() (verifier, challenge string, err error) {
	verifier, err = randomURLToken()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomURLToken() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("login: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func buildAuthURL(authEP, clientID, redirectURL string, scopes []string, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURL},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(authEP, "?") {
		sep = "&"
	}
	return authEP + sep + q.Encode()
}

type callbackOutcome struct {
	code string
	err  error
}

// callbackHandler validates the OAuth redirect and reports the outcome on ch
// exactly once. It only acts on the configured path so probes elsewhere are 404s.
func callbackHandler(path, wantState string, ch chan<- callbackOutcome) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			writeBrowserPage(w, "Login failed", "Authorization failed — you can close this tab and check the terminal.")
			ch <- callbackOutcome{err: fmt.Errorf("login: authorization error: %s", msg)}
			return
		}
		if got := q.Get("state"); got != wantState {
			writeBrowserPage(w, "Login failed", "State mismatch — you can close this tab and try again.")
			ch <- callbackOutcome{err: fmt.Errorf("login: state mismatch (possible CSRF or a stale tab)")}
			return
		}
		code := q.Get("code")
		if code == "" {
			writeBrowserPage(w, "Login failed", "No authorization code returned — close this tab and retry.")
			ch <- callbackOutcome{err: fmt.Errorf("login: callback had no authorization code")}
			return
		}
		writeBrowserPage(w, "Login complete", "You can close this tab and return to the terminal.")
		ch <- callbackOutcome{code: code}
	}
}

// awaitCallback binds the loopback listener and blocks until the IdP redirects
// back (or ctx is cancelled / times out).
func awaitCallback(ctx context.Context, redirect loopbackRedirect, wantState string) (string, error) {
	ln, err := net.Listen("tcp", redirect.addr)
	if err != nil {
		return "", fmt.Errorf("login: cannot listen on %s for the callback: %w", redirect.addr, err)
	}
	ch := make(chan callbackOutcome, 1)
	srv := &http.Server{
		Handler:           callbackHandler(redirect.path, wantState, ch),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("login: timed out waiting for the callback: %w", ctx.Err())
	case out := <-ch:
		return out.code, out.err
	}
}

type tokenResponse struct {
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrDesc      string `json:"error_description"`
}

func exchangeCode(ctx context.Context, client *http.Client, tokenEP string, opts Options, code, verifier, redirectURL string) (Result, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"client_id":     {opts.ClientID},
		"code_verifier": {verifier},
	}
	if opts.ClientSecret != "" {
		form.Set("client_secret", opts.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEP, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, fmt.Errorf("login: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("login: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("login: read token response: %w", err)
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return Result{}, fmt.Errorf("login: decode token response: %w", err)
	}
	if tok.Error != "" {
		msg := tok.Error
		if tok.ErrDesc != "" {
			msg += ": " + tok.ErrDesc
		}
		return Result{}, fmt.Errorf("login: token endpoint error: %s", msg)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("login: token endpoint returned %d", resp.StatusCode)
	}
	sub, email := decodeSubEmail(tok.IDToken)
	return Result{
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		AccessToken:  tok.AccessToken,
		ExpiresIn:    tok.ExpiresIn,
		Subject:      sub,
		Email:        email,
	}, nil
}

// decodeSubEmail best-effort reads sub/email from an id_token payload without
// verifying the signature (informational display only).
func decodeSubEmail(idToken string) (sub, email string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return claims.Sub, claims.Email
}

func writeBrowserPage(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><html><head><meta charset=utf-8><title>"+title+
		"</title></head><body style=\"font-family:system-ui;margin:3rem;text-align:center\"><h2>"+
		title+"</h2><p>"+body+"</p></body></html>")
}

// openBrowser best-effort launches the system browser at url.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
