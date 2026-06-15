// Command wardrowbe-mcp serves the Wardrowbe wardrobe API as MCP tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/mcpserver"
	"github.com/jansitarski/wardrowbe-mcp/internal/oidclogin"
	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	err := run(os.Args[1:])
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		// Usage was already printed by the flag package; --help is not a failure.
	case errors.Is(err, config.ErrUsage):
		// The flag package already reported the parse error and usage to stderr.
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// `login` is an interactive subcommand for minting a refresh token; the
	// default (no subcommand) runs the server.
	if len(args) > 0 && args[0] == "login" {
		return runLogin(args[1:])
	}
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Println(mcpserver.Version())
		return nil
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	provider, err := buildProvider(cfg)
	if err != nil {
		return err
	}

	httpClient := &http.Client{
		Timeout: 5 * time.Minute, // outfit suggestions can be slow (Ollama on CPU)
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// All traffic targets one backend host; Go's default of 2 idle
			// conns/host would defeat keep-alive under concurrency. Size the
			// pool to the request concurrency cap and ceiling total conns so a
			// stalled backend can't accumulate unbounded connections.
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: cfg.MaxConcurrent,
			MaxConnsPerHost:     cfg.MaxConcurrent * 2,
		},
	}
	client := wardrowbe.NewClient(cfg.WardrowbeURL, provider, httpClient, logger)
	srv := mcpserver.New(cfg, client, logger)

	switch cfg.Transport {
	case config.TransportStdio:
		logger.Info("starting wardrowbe-mcp on stdio")
		return server.ServeStdio(srv.MCP())
	case config.TransportHTTP:
		return serveHTTP(cfg, srv, logger)
	default:
		return fmt.Errorf("unsupported transport %q", cfg.Transport)
	}
}

func serveHTTP(cfg config.Config, srv *mcpserver.Server, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.AuthMode == config.AuthDev {
		// Dev auth sends a fixed identity to the backend — every caller is treated
		// as the same user. Fine for a single-user homelab, dangerous if this is
		// unknowingly exposed to multiple people. Make it impossible to miss.
		logger.Warn("running with dev auth on http transport: all requests use a fixed identity; "+
			"use --auth oidc for real per-user identity", "external_id", cfg.ExternalID)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,  // full inbound request (incl. base64 uploads)
		WriteTimeout:      6 * time.Minute,   // > backend client timeout (slow Ollama)
		IdleTimeout:       120 * time.Second, // keep-alive idle ceiling
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting wardrowbe-mcp", "transport", "http", "addr", cfg.Addr(),
			"auth", cfg.AuthMode, "external_id", cfg.ExternalID)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Restore default signal handling so a second Ctrl-C force-exits instead
		// of being swallowed while we drain.
		stop()
		logger.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				// Not the drain timeout — a real shutdown failure; report it.
				return err
			}
			// In-flight requests can legitimately outlive the drain window (a slow
			// outfit suggestion runs minutes). Abandoning them on a routine SIGTERM
			// is expected, not fatal — close the remaining connections and exit 0.
			logger.Warn("drain window elapsed; closing remaining connections", "err", err)
			_ = httpSrv.Close()
		}
		return nil
	}
}

// runLogin performs the interactive Authorization Code + PKCE loopback flow and
// prints the resulting refresh token. It mints the credential the headless
// server later uses; the server itself can never do this (no browser).
func runLogin(args []string) error {
	fs := flag.NewFlagSet("wardrowbe-mcp login", flag.ContinueOnError)
	issuer := fs.String("oidc-issuer-url", os.Getenv("MCP_OIDC_ISSUER_URL"), "OIDC issuer URL (used for discovery)")
	clientID := fs.String("oidc-client-id", os.Getenv("MCP_OIDC_CLIENT_ID"), "OAuth client id (required)")
	// Secret has no env-seeded default so it never prints in --help/usage; env is
	// applied after parsing.
	clientSecret := fs.String("oidc-client-secret", "", "OAuth client secret for confidential clients (prefer env MCP_OIDC_CLIENT_SECRET)")
	authEndpoint := fs.String("oidc-auth-endpoint", os.Getenv("MCP_OIDC_AUTH_ENDPOINT"), "authorization endpoint override (skips discovery)")
	tokenEndpoint := fs.String("oidc-token-endpoint", os.Getenv("MCP_OIDC_TOKEN_ENDPOINT"), "token endpoint override (skips discovery)")
	redirectURL := fs.String("redirect-url", "http://127.0.0.1:8976/callback", "loopback redirect URL; must be registered at the IdP")
	scopes := fs.String("scopes", "openid email profile offline_access", "space-separated scopes (include offline_access to get a refresh token)")
	tokenFile := fs.String("oidc-refresh-token-file", "", "also write the refresh token to this file (0600)")
	noBrowser := fs.Bool("no-browser", false, "do not try to open a browser automatically")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for the browser callback")
	if err := fs.Parse(args); err != nil {
		return err // flag printed usage; flag.ErrHelp is handled in main()
	}
	if *clientSecret == "" {
		*clientSecret = os.Getenv("MCP_OIDC_CLIENT_SECRET")
	}
	if *clientID == "" {
		return fmt.Errorf("login: --oidc-client-id (MCP_OIDC_CLIENT_ID) is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	res, err := oidclogin.Run(ctx, oidclogin.Options{
		Issuer:        *issuer,
		ClientID:      *clientID,
		ClientSecret:  *clientSecret,
		AuthEndpoint:  *authEndpoint,
		TokenEndpoint: *tokenEndpoint,
		RedirectURL:   *redirectURL,
		Scopes:        strings.Fields(*scopes),
		OpenBrowser:   !*noBrowser,
	}, os.Stderr)
	if err != nil {
		return err
	}

	if res.Subject != "" {
		ident := "sub=" + res.Subject
		if res.Email != "" {
			ident += " email=" + res.Email
		}
		fmt.Fprintln(os.Stderr, "Authenticated:", ident)
	}
	if res.RefreshToken == "" {
		fmt.Fprintln(os.Stderr, "WARNING: the IdP returned no refresh_token. Enable refresh tokens on the "+
			"app and include the offline_access scope (--scopes). Only short-lived tokens were issued.")
		return nil
	}
	if *tokenFile != "" {
		if err := os.WriteFile(*tokenFile, []byte(res.RefreshToken), 0o600); err != nil {
			return fmt.Errorf("login: write %s: %w", *tokenFile, err)
		}
		fmt.Fprintln(os.Stderr, "Refresh token written to", *tokenFile)
	}
	// Print the refresh token to stdout so it can be captured/piped cleanly,
	// separate from the human-facing messages on stderr.
	fmt.Println(res.RefreshToken)
	return nil
}

func buildProvider(cfg config.Config) (wardrowbe.TokenProvider, error) {
	switch cfg.AuthMode {
	case config.AuthDev:
		return wardrowbe.DevTokenProvider{
			ExternalID:  cfg.ExternalID,
			Email:       cfg.ExternalEmail,
			DisplayName: cfg.ExternalID,
		}, nil
	case config.AuthOIDC:
		return &wardrowbe.OIDCTokenProvider{
			Issuer:           cfg.OIDCIssuerURL,
			ClientID:         cfg.OIDCClientID,
			ClientSecret:     cfg.OIDCClientSecret,
			RefreshToken:     cfg.OIDCRefreshToken,
			RefreshTokenFile: cfg.OIDCRefreshTokenFile,
			IDToken:          cfg.OIDCIDToken,
			TokenEndpoint:    cfg.OIDCTokenEndpoint,
			HTTPClient:       &http.Client{Timeout: 30 * time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", cfg.AuthMode)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "DEBUG":
		lvl = slog.LevelDebug
	case "WARN", "WARNING":
		lvl = slog.LevelWarn
	case "ERROR":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
