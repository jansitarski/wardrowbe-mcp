// Command wardrowbe-mcp serves the Wardrowbe wardrobe API as MCP tools.
package main

import (
	"context"
	"errors"
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
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
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
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
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

	httpSrv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
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
		logger.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
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
		return wardrowbe.OIDCTokenProvider{
			Issuer:       cfg.OIDCIssuerURL,
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RefreshToken: cfg.OIDCRefreshToken,
			HTTPClient:   &http.Client{Timeout: 30 * time.Second},
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
