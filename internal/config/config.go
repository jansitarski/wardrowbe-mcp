// Package config parses the wardrowbe-mcp configuration surface from flags and
// environment variables. Flags take precedence over env vars.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ErrUsage wraps flag-parsing errors that the flag package has already reported
// to stderr (unknown flag, bad value, ...). Callers should exit non-zero without
// printing the error again.
var ErrUsage = errors.New("usage error")

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

	OIDCIssuerURL     string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRefreshToken  string
	OIDCIDToken       string // static id_token, used when no refresh token is set
	OIDCTokenEndpoint string // optional override; skips discovery when set

	LogLevel string

	ImageMaxDim       int
	ImageVariant      ImageVariant
	PortalResourceURL string

	// MaxConcurrent bounds in-flight /mcp requests (http transport); excess
	// requests get 503. MaxBodyBytes caps the inbound /mcp request body.
	MaxConcurrent int
	MaxBodyBytes  int64

	// ShowVersion is set by --version; when true the caller should print the
	// version and exit instead of starting the server. The rest of the Config is
	// not validated in that case.
	ShowVersion bool
}

// Load resolves configuration from the given args (typically os.Args[1:]) and
// the process environment. Flags override env; env overrides defaults.
func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("wardrowbe-mcp", flag.ContinueOnError)

	// Collect malformed integer env vars instead of silently falling back to the
	// default, so a typo (e.g. MCP_MAX_CONCURRENT=abc) fails loudly at startup.
	// Each error is keyed by its flag name: an explicitly-passed flag overrides
	// the env var (documented precedence), so its env error must not be fatal.
	type envErr struct{ flagName, msg string }
	var envErrs []envErr
	envOrInt := func(flagName, key string, fallback int) int {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			envErrs = append(envErrs, envErr{flagName, fmt.Sprintf("%s=%q (not an integer)", key, v)})
			return fallback
		}
		return n
	}

	transport := fs.String("transport", envOr("MCP_TRANSPORT", defaultTransport), "transport: http or stdio")
	host := fs.String("host", envOr("MCP_BIND_HOST", defaultHost), "bind host (http)")
	port := fs.Int("port", envOrInt("port", "MCP_BIND_PORT", defaultPort), "bind port (http)")

	wardrowbeURL := fs.String("wardrowbe-url", envOr("WARDROWBE_URL", ""), "backend base URL (no /api/v1); required")

	// Secret-bearing flags must NOT seed their defaults from the environment:
	// the flag package prints non-empty defaults in its usage output, which is
	// emitted on --help and on any mistyped flag — dumping secrets to stderr
	// (and into container logs on a crash loop). They get empty defaults here
	// and fall back to env after parsing, preserving flags-over-env precedence.
	apiKey := fs.String("api-key", "", "incoming Bearer key (required for http; prefer env MCP_API_KEY)")

	authMode := fs.String("auth", envOr("MCP_AUTH_MODE", defaultAuthMode), "auth mode: dev or oidc")
	externalID := fs.String("external-id", envOr("MCP_EXTERNAL_ID", defaultExternalID), "dev identity external_id")
	externalEmail := fs.String("external-email", envOr("MCP_EXTERNAL_EMAIL", ""), "dev sync email (defaults to <external-id>@wardrowbe.local)")

	oidcIssuer := fs.String("oidc-issuer-url", envOr("MCP_OIDC_ISSUER_URL", ""), "OIDC issuer URL")
	oidcClientID := fs.String("oidc-client-id", envOr("MCP_OIDC_CLIENT_ID", ""), "OIDC client id")
	oidcTokenEndpoint := fs.String("oidc-token-endpoint", envOr("MCP_OIDC_TOKEN_ENDPOINT", ""), "OIDC token endpoint override (skips discovery)")
	oidcClientSecret := fs.String("oidc-client-secret", "", "OIDC client secret (prefer env MCP_OIDC_CLIENT_SECRET)")
	oidcRefreshToken := fs.String("oidc-refresh-token", "", "OIDC refresh token, enables the refresh_token grant (prefer env MCP_OIDC_REFRESH_TOKEN)")
	oidcIDToken := fs.String("oidc-id-token", "", "static OIDC id_token, used when no refresh token is set (prefer env MCP_OIDC_ID_TOKEN)")

	logLevel := fs.String("log-level", envOr("MCP_LOG_LEVEL", defaultLogLevel), "log level")

	imageMaxDim := fs.Int("image-max-dim", envOrInt("image-max-dim", "MCP_IMAGE_MAX_DIM", defaultImageMaxDim), "max returned image dimension")
	imageVariant := fs.String("image-default-variant", envOr("MCP_IMAGE_VARIANT", defaultImageVariant), "default image variant: thumb/medium/full")
	portalResourceURL := fs.String("portal-resource-url", envOr("MCP_PORTAL_RESOURCE_URL", ""), "OAuth protected-resource metadata URL for WWW-Authenticate")

	maxConcurrent := fs.Int("max-concurrent", envOrInt("max-concurrent", "MCP_MAX_CONCURRENT", defaultMaxConcurrent), "max in-flight /mcp requests (http)")
	maxBodyMB := fs.Int("max-body-mb", envOrInt("max-body-mb", "MCP_MAX_BODY_MB", defaultMaxBodyMB), "max inbound /mcp request body in MiB")

	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, err
		}
		// The flag package already printed the error and usage to stderr.
		return Config{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if *showVersion {
		// Skip validation: --version must work without --api-key etc.
		return Config{ShowVersion: true}, nil
	}
	// A malformed integer env var is fatal only when its value would actually be
	// used: a flag passed on the command line wins over the env var, so its env
	// error is dropped (the stale env value never influences the config).
	explicitly := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicitly[f.Name] = true })
	var fatalEnvErrs []string
	for _, e := range envErrs {
		if !explicitly[e.flagName] {
			fatalEnvErrs = append(fatalEnvErrs, e.msg)
		}
	}
	if len(fatalEnvErrs) > 0 {
		return Config{}, fmt.Errorf("invalid integer environment variable(s): %s", strings.Join(fatalEnvErrs, ", "))
	}

	// Apply env fallbacks for the secret flags that intentionally have no
	// env-seeded default (see above). An explicitly-set flag still wins.
	for _, sec := range []struct {
		dst    *string
		envKey string
	}{
		{apiKey, "MCP_API_KEY"},
		{oidcClientSecret, "MCP_OIDC_CLIENT_SECRET"},
		{oidcRefreshToken, "MCP_OIDC_REFRESH_TOKEN"},
		{oidcIDToken, "MCP_OIDC_ID_TOKEN"},
	} {
		if *sec.dst == "" {
			*sec.dst = envOr(sec.envKey, "")
		}
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
		OIDCTokenEndpoint: *oidcTokenEndpoint,
		OIDCClientSecret:  *oidcClientSecret,
		OIDCRefreshToken:  *oidcRefreshToken,
		OIDCIDToken:       *oidcIDToken,
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
		if strings.TrimSpace(c.ExternalID) == "" {
			return errors.New("dev mode requires a non-empty --external-id (MCP_EXTERNAL_ID)")
		}
	case AuthOIDC:
		// The id_token comes from one of two sources. The refresh_token grant is
		// the durable path; a static id_token is the fallback for issuers that do
		// not issue refresh tokens (it expires and is not renewed).
		if c.OIDCRefreshToken == "" && c.OIDCIDToken == "" {
			return errors.New("oidc mode requires one of --oidc-refresh-token (refresh_token grant) or --oidc-id-token (static token)")
		}
		// Only the refresh_token grant contacts the issuer (discovery + token
		// endpoint), so it needs the issuer and client id. A static id_token is
		// forwarded as-is and never touches the issuer, so those are optional on
		// that path.
		if c.OIDCRefreshToken != "" && (c.OIDCIssuerURL == "" || c.OIDCClientID == "") {
			return errors.New("oidc mode with --oidc-refresh-token requires --oidc-issuer-url and --oidc-client-id")
		}
		// When an issuer is set it is the TLS origin the client secret and refresh
		// token are POSTed to (via discovery), so it must be https and well-formed.
		if c.OIDCIssuerURL != "" {
			if u, err := url.Parse(c.OIDCIssuerURL); err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("invalid --oidc-issuer-url %q (must be an https URL)", c.OIDCIssuerURL)
			}
		}
		// Same for an explicit token-endpoint override.
		if c.OIDCTokenEndpoint != "" {
			if u, err := url.Parse(c.OIDCTokenEndpoint); err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("invalid --oidc-token-endpoint %q (must be an https URL)", c.OIDCTokenEndpoint)
			}
		}
	default:
		return fmt.Errorf("invalid --auth %q (want dev or oidc)", c.AuthMode)
	}

	switch c.ImageVariant {
	case VariantThumb, VariantMedium, VariantFull:
	default:
		return fmt.Errorf("invalid --image-default-variant %q (want thumb/medium/full)", c.ImageVariant)
	}

	// LogLevel is uppercased in Load; reject anything the logger would silently
	// map to INFO so a typo (e.g. "DEBG") fails fast instead of hiding output.
	switch c.LogLevel {
	case "DEBUG", "INFO", "WARN", "WARNING", "ERROR":
	default:
		return fmt.Errorf("invalid --log-level %q (want debug/info/warn/error)", c.LogLevel)
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
	// Validate it now so a typo'd/wrong-scheme URL fails at startup with a clear
	// message instead of an opaque error on the first backend request.
	if u, err := url.Parse(c.WardrowbeURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid --wardrowbe-url %q (want an http(s) URL like http://host:8000)", c.WardrowbeURL)
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
