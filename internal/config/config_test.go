package config

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

// allEnvKeys lists every environment variable Load reads, so tests can run
// hermetically regardless of what is set in the developer's shell.
var allEnvKeys = []string{
	"MCP_TRANSPORT", "MCP_BIND_HOST", "MCP_BIND_PORT",
	"WARDROWBE_URL", "MCP_API_KEY",
	"MCP_AUTH_MODE", "MCP_EXTERNAL_ID", "MCP_EXTERNAL_EMAIL",
	"MCP_OIDC_ISSUER_URL", "MCP_OIDC_CLIENT_ID", "MCP_OIDC_TOKEN_ENDPOINT",
	"MCP_OIDC_CLIENT_SECRET", "MCP_OIDC_REFRESH_TOKEN", "MCP_OIDC_REFRESH_TOKEN_FILE",
	"MCP_LOG_LEVEL", "MCP_IMAGE_MAX_DIM", "MCP_IMAGE_VARIANT",
	"MCP_PORTAL_RESOURCE_URL", "MCP_MAX_BODY_MB", "MCP_STATELESS",
}

// clearEnv blanks every config env var for the duration of the test (envOr
// treats empty as unset).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allEnvKeys {
		t.Setenv(k, "")
	}
}

// baseArgs returns a minimal valid argument list tests can extend.
func baseArgs(extra ...string) []string {
	return append([]string{"--wardrowbe-url", "http://backend:8000"}, extra...)
}

func TestLoadDefaultsForHTTPRequireAPIKey(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs("--transport", "http"))
	if err == nil {
		t.Fatal("expected error when api key missing for http transport")
	}
}

func TestWardrowbeURLRequired(t *testing.T) {
	clearEnv(t)
	_, err := Load([]string{"--api-key", "k"})
	if err == nil || !strings.Contains(err.Error(), "wardrowbe-url") {
		t.Fatalf("expected missing-wardrowbe-url error, got: %v", err)
	}
}

func TestInvalidIntEnvRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "k")
	t.Setenv("MCP_MAX_BODY_MB", "not-a-number")
	_, err := Load(baseArgs("--transport", "http"))
	if err == nil {
		t.Fatal("expected error for non-integer MCP_MAX_BODY_MB")
	}
}

func TestInvalidIntEnvIgnoredWhenFlagOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "k")
	t.Setenv("MCP_BIND_PORT", "808O") // typo'd, but overridden by --port
	cfg, err := Load(baseArgs("--transport", "http", "--port", "8080"))
	if err != nil {
		t.Fatalf("flag overrides the bad env var, so Load should succeed: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("got port %d, want 8080", cfg.Port)
	}
}

func TestStatelessDefaultsTrue(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs("--api-key", "k"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Stateless {
		t.Error("Stateless should default to true")
	}
}

func TestStatelessEnvDisables(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_STATELESS", "false")
	cfg, err := Load(baseArgs("--api-key", "k"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Stateless {
		t.Error("MCP_STATELESS=false should disable stateless mode")
	}
}

func TestStatelessFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_STATELESS", "false")
	cfg, err := Load(baseArgs("--api-key", "k", "--stateless=true"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Stateless {
		t.Error("--stateless=true should override MCP_STATELESS=false")
	}
}

func TestInvalidBoolEnvRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "k")
	t.Setenv("MCP_STATELESS", "yes-please")
	_, err := Load(baseArgs())
	if err == nil {
		t.Fatal("expected error for non-boolean MCP_STATELESS")
	}
}

func TestInvalidBoolEnvIgnoredWhenFlagOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "k")
	t.Setenv("MCP_STATELESS", "yes-please") // malformed, but overridden by --stateless
	cfg, err := Load(baseArgs("--stateless=false"))
	if err != nil {
		t.Fatalf("flag overrides the bad env var, so Load should succeed: %v", err)
	}
	if cfg.Stateless {
		t.Error("explicit --stateless=false should win")
	}
}

func TestInvalidLogLevelRejected(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs("--api-key", "k", "--log-level", "debg"))
	if err == nil {
		t.Fatal("expected error for unknown log level")
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_BIND_PORT", "9999")
	t.Setenv("MCP_API_KEY", "from-env")
	cfg, err := Load(baseArgs("--port", "8081"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8081 {
		t.Errorf("flag should override env: got port %d, want 8081", cfg.Port)
	}
	if cfg.APIKey != "from-env" {
		t.Errorf("env api key not used: got %q", cfg.APIKey)
	}
}

func TestSecretFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "from-env")
	cfg, err := Load(baseArgs("--api-key", "from-flag"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "from-flag" {
		t.Errorf("flag should override env for secrets: got %q", cfg.APIKey)
	}
}

// TestUsageOutputNeverContainsSecrets guards against re-seeding secret flag
// defaults from the environment: the flag package prints non-empty defaults in
// its usage text, which is emitted on --help and on any mistyped flag.
func TestUsageOutputNeverContainsSecrets(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_API_KEY", "super-secret-api-key")
	t.Setenv("MCP_OIDC_CLIENT_SECRET", "super-secret-client-secret")
	t.Setenv("MCP_OIDC_REFRESH_TOKEN", "super-secret-refresh-token")

	// A mistyped flag makes the flag package print usage (with defaults) and
	// Load report ErrUsage. The usage text cannot contain the secrets because
	// the secret flags' defaults are empty (env applies only after parsing).
	_, err := Load([]string{"--no-such-flag"})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("expected ErrUsage for unknown flag, got: %v", err)
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Errorf("usage error leaked a secret: %v", err)
	}

	cfg, err := Load(baseArgs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "super-secret-api-key" {
		t.Errorf("api key not resolved from env after parse: got %q", cfg.APIKey)
	}
	if cfg.OIDCClientSecret != "super-secret-client-secret" {
		t.Errorf("oidc client secret not resolved from env: got %q", cfg.OIDCClientSecret)
	}
	if cfg.OIDCRefreshToken != "super-secret-refresh-token" {
		t.Errorf("oidc refresh token not resolved from env: got %q", cfg.OIDCRefreshToken)
	}
}

func TestHelpReturnsFlagErrHelp(t *testing.T) {
	clearEnv(t)
	_, err := Load([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got: %v", err)
	}
}

func TestVersionFlagSkipsValidation(t *testing.T) {
	clearEnv(t)
	cfg, err := Load([]string{"--version"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ShowVersion {
		t.Error("ShowVersion not set")
	}
}

func TestExternalEmailDefaultsFromID(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs("--api-key", "k", "--external-id", "alice-example-com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alice-example-com@wardrowbe.local"
	if cfg.ExternalEmail != want {
		t.Errorf("default external email: got %q, want %q", cfg.ExternalEmail, want)
	}
}

func TestExternalEmailExplicitWins(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs("--api-key", "k", "--external-id", "x", "--external-email", "real@example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExternalEmail != "real@example.com" {
		t.Errorf("explicit external email: got %q", cfg.ExternalEmail)
	}
}

func TestDevModeRejectsEmptyExternalID(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs("--api-key", "k", "--external-id", ""))
	if err == nil {
		t.Fatal("expected error for empty --external-id in dev mode")
	}
}

func TestStdioDoesNotRequireAPIKey(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs("--transport", "stdio"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("got transport %q", cfg.Transport)
	}
}

func TestInvalidVariantRejected(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs("--api-key", "k", "--image-default-variant", "huge"))
	if err == nil {
		t.Fatal("expected error for invalid image variant")
	}
}

func TestOIDCRequiresFields(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs("--api-key", "k", "--auth", "oidc"))
	if err == nil {
		t.Fatal("expected error: oidc mode missing required fields")
	}
}

func TestInvalidWardrowbeURLRejected(t *testing.T) {
	clearEnv(t)
	for _, u := range []string{"not-a-url", "postgres://db/x", "/just/a/path"} {
		if _, err := Load([]string{"--api-key", "k", "--wardrowbe-url", u}); err == nil {
			t.Errorf("expected error for --wardrowbe-url %q", u)
		}
	}
}

func TestOIDCIssuerMustBeHTTPS(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "http://issuer.example.com",
		"--oidc-client-id", "c", "--oidc-refresh-token", "r",
	))
	if err == nil {
		t.Fatal("expected error: oidc issuer must be https")
	}
}

func TestOIDCTokenEndpointMustBeHTTPS(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "https://issuer.example.com",
		"--oidc-client-id", "c", "--oidc-refresh-token", "r",
		"--oidc-token-endpoint", "http://token.example.com/token",
	))
	if err == nil {
		t.Fatal("expected error: oidc token endpoint override must be https")
	}
}

func TestOIDCValidHTTPSIssuerAccepted(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "https://issuer.example.com",
		"--oidc-client-id", "c", "--oidc-refresh-token", "r",
		"--oidc-token-endpoint", "https://token.example.com/token",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthOIDC {
		t.Errorf("got auth mode %q", cfg.AuthMode)
	}
	if cfg.OIDCTokenEndpoint != "https://token.example.com/token" {
		t.Errorf("token endpoint override not stored: %q", cfg.OIDCTokenEndpoint)
	}
}

func TestOIDCRequiresATokenSource(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "https://issuer.example.com",
		"--oidc-client-id", "c",
	))
	if err == nil {
		t.Fatal("expected error: oidc mode missing both refresh token and id_token")
	}
}

func TestOIDCRefreshTokenFileIsATokenSource(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "https://issuer.example.com",
		"--oidc-client-id", "c", "--oidc-refresh-token-file", "/var/run/mcp/rt",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDCRefreshTokenFile != "/var/run/mcp/rt" {
		t.Errorf("refresh token file not stored: %q", cfg.OIDCRefreshTokenFile)
	}
}

func TestOIDCStaticIDTokenAccepted(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-issuer-url", "https://issuer.example.com",
		"--oidc-client-id", "c", "--oidc-id-token", "header.payload.sig",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDCRefreshToken != "" || cfg.OIDCIDToken != "header.payload.sig" {
		t.Errorf("unexpected oidc token config: refresh=%q id=%q", cfg.OIDCRefreshToken, cfg.OIDCIDToken)
	}
}

// TestOIDCStaticIDTokenWithoutIssuerAccepted: the static path never contacts the
// issuer, so issuer/client id are not required when only --oidc-id-token is set.
func TestOIDCStaticIDTokenWithoutIssuerAccepted(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-id-token", "header.payload.sig",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDCIDToken != "header.payload.sig" {
		t.Errorf("static id_token not stored: %q", cfg.OIDCIDToken)
	}
}

// TestOIDCRefreshTokenStillRequiresIssuer: the refresh_token grant does contact
// the issuer, so issuer/client id remain mandatory on that path.
func TestOIDCRefreshTokenStillRequiresIssuer(t *testing.T) {
	clearEnv(t)
	_, err := Load(baseArgs(
		"--api-key", "k", "--auth", "oidc",
		"--oidc-refresh-token", "r",
	))
	if err == nil {
		t.Fatal("expected error: refresh_token grant requires issuer and client id")
	}
}
