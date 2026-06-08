// Package config parses the wardrowbe-mcp configuration surface from flags and
// environment variables. Flags take precedence over env vars.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Transport identifies how the MCP server talks to clients.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportStdio Transport = "stdio"
)

// AuthMode selects how the backend JWT is obtained.
type AuthMode string

const (
	AuthDev  AuthMode = "dev"
	AuthOIDC AuthMode = "oidc"
)

// ImageVariant selects which stored image size is returned by default.
type ImageVariant string

const (
	VariantThumb  ImageVariant = "thumb"
	VariantMedium ImageVariant = "medium"
	VariantFull   ImageVariant = "full"
)

// Defaults mirror the Python server plus the new Go-only knobs.
const (
	defaultTransport     = string(TransportHTTP)
	defaultHost          = "0.0.0.0"
	defaultPort          = 8080
	defaultWardrowbeURL  = "http://127.0.0.1:8000"
	defaultAuthMode      = string(AuthDev)
	defaultExternalID    = "wardrowbe-mcp"
	defaultLogLevel      = "INFO"
	defaultImageMaxDim   = 768
	defaultImageVariant  = string(VariantMedium)
	externalEmailSuffix  = "@wardrowbe.local"
	defaultMaxConcurrent = 16
	defaultMaxBodyMB     = 40
)

// Config holds the fully-resolved runtime configuration.
type Config struct {
	Transport Transport
	Host      string
	Port      int

	WardrowbeURL string
	APIKey       string

	AuthMode      AuthMode
	ExternalID    string
	ExternalEmail string

	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRefreshToken string

	LogLevel string

	ImageMaxDim       int
	ImageVariant      ImageVariant
	PortalResourceURL string

	// MaxConcurrent bounds in-flight /mcp requests (http transport); excess
	// requests get 503. MaxBodyBytes caps the inbound /mcp request body.
	MaxConcurrent int
	MaxBodyBytes  int64
}

// Load resolves configuration from the given args (typically os.Args[1:]) and
// the process environment. Flags override env; env overrides defaults.
func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("wardrowbe-mcp", flag.ContinueOnError)

	transport := fs.String("transport", envOr("MCP_TRANSPORT", defaultTransport), "transport: http or stdio")
	host := fs.String("host", envOr("MCP_BIND_HOST", defaultHost), "bind host (http)")
	port := fs.Int("port", envOrInt("MCP_BIND_PORT", defaultPort), "bind port (http)")

	wardrowbeURL := fs.String("wardrowbe-url", envOr("WARDROWBE_URL", defaultWardrowbeURL), "backend base URL (no /api/v1)")
	apiKey := fs.String("api-key", envOr("MCP_API_KEY", ""), "incoming Bearer key (required for http)")

	authMode := fs.String("auth", envOr("MCP_AUTH_MODE", defaultAuthMode), "auth mode: dev or oidc")
	externalID := fs.String("external-id", envOr("MCP_EXTERNAL_ID", defaultExternalID), "dev identity external_id")
	externalEmail := fs.String("external-email", envOr("MCP_EXTERNAL_EMAIL", ""), "dev sync email (defaults to <external-id>@wardrowbe.local)")

	oidcIssuer := fs.String("oidc-issuer-url", envOr("MCP_OIDC_ISSUER_URL", ""), "OIDC issuer URL")
	oidcClientID := fs.String("oidc-client-id", envOr("MCP_OIDC_CLIENT_ID", ""), "OIDC client id")
	oidcClientSecret := fs.String("oidc-client-secret", envOr("MCP_OIDC_CLIENT_SECRET", ""), "OIDC client secret")
	oidcRefreshToken := fs.String("oidc-refresh-token", envOr("MCP_OIDC_REFRESH_TOKEN", ""), "OIDC refresh token")

	logLevel := fs.String("log-level", envOr("MCP_LOG_LEVEL", defaultLogLevel), "log level")

	imageMaxDim := fs.Int("image-max-dim", envOrInt("MCP_IMAGE_MAX_DIM", defaultImageMaxDim), "max returned image dimension")
	imageVariant := fs.String("image-default-variant", envOr("MCP_IMAGE_VARIANT", defaultImageVariant), "default image variant: thumb/medium/full")
	portalResourceURL := fs.String("portal-resource-url", envOr("MCP_PORTAL_RESOURCE_URL", ""), "OAuth protected-resource metadata URL for WWW-Authenticate")

	maxConcurrent := fs.Int("max-concurrent", envOrInt("MCP_MAX_CONCURRENT", defaultMaxConcurrent), "max in-flight /mcp requests (http)")
	maxBodyMB := fs.Int("max-body-mb", envOrInt("MCP_MAX_BODY_MB", defaultMaxBodyMB), "max inbound /mcp request body in MiB")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Transport:         Transport(strings.ToLower(*transport)),
		Host:              *host,
		Port:              *port,
		WardrowbeURL:      strings.TrimRight(*wardrowbeURL, "/"),
		APIKey:            *apiKey,
		AuthMode:          AuthMode(strings.ToLower(*authMode)),
		ExternalID:        *externalID,
		ExternalEmail:     *externalEmail,
		OIDCIssuerURL:     *oidcIssuer,
		OIDCClientID:      *oidcClientID,
		OIDCClientSecret:  *oidcClientSecret,
		OIDCRefreshToken:  *oidcRefreshToken,
		LogLevel:          strings.ToUpper(*logLevel),
		ImageMaxDim:       *imageMaxDim,
		ImageVariant:      ImageVariant(strings.ToLower(*imageVariant)),
		PortalResourceURL: *portalResourceURL,
		MaxConcurrent:     *maxConcurrent,
		MaxBodyBytes:      int64(*maxBodyMB) << 20,
	}

	if cfg.ExternalEmail == "" {
		cfg.ExternalEmail = cfg.ExternalID + externalEmailSuffix
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	switch c.Transport {
	case TransportHTTP, TransportStdio:
	default:
		return fmt.Errorf("invalid --transport %q (want http or stdio)", c.Transport)
	}

	switch c.AuthMode {
	case AuthDev:
	case AuthOIDC:
		if c.OIDCIssuerURL == "" || c.OIDCClientID == "" || c.OIDCRefreshToken == "" {
			return errors.New("oidc mode requires --oidc-issuer-url, --oidc-client-id and --oidc-refresh-token")
		}
	default:
		return fmt.Errorf("invalid --auth %q (want dev or oidc)", c.AuthMode)
	}

	switch c.ImageVariant {
	case VariantThumb, VariantMedium, VariantFull:
	default:
		return fmt.Errorf("invalid --image-default-variant %q (want thumb/medium/full)", c.ImageVariant)
	}

	if c.Transport == TransportHTTP {
		if c.APIKey == "" {
			return errors.New("--api-key (MCP_API_KEY) is required for http transport")
		}
		if c.Port <= 0 || c.Port > 65535 {
			return fmt.Errorf("invalid --port %d", c.Port)
		}
	}

	if c.WardrowbeURL == "" {
		return errors.New("--wardrowbe-url (WARDROWBE_URL) is required")
	}
	if c.ImageMaxDim <= 0 {
		return fmt.Errorf("invalid --image-max-dim %d", c.ImageMaxDim)
	}
	if c.MaxConcurrent <= 0 {
		return fmt.Errorf("invalid --max-concurrent %d (must be > 0)", c.MaxConcurrent)
	}
	if c.MaxBodyBytes <= 0 {
		return fmt.Errorf("invalid --max-body-mb (must be > 0)")
	}
	return nil
}

// Addr returns the host:port the http server binds to.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
