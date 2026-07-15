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
	"syscall"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/mcpserver"
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
			// conns/host would defeat keep-alive under concurrency. Ceiling
			// total conns so a stalled backend can't accumulate unbounded
			// connections.
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 16,
			MaxConnsPerHost:     32,
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
		ReadTimeout:       60 * time.Second, // full inbound request (incl. base64 uploads)
		// No WriteTimeout: it spans the whole response lifetime, so it would
		// hard-kill any standing (heartbeated) SSE stream at exactly that mark
		// regardless of activity. Handler work is already bounded by the backend
		// client's 5-minute timeout, and a dead peer surfaces as a failed
		// heartbeat/TCP write rather than lingering forever.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second, // keep-alive idle ceiling
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
