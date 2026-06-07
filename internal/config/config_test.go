package config

import "testing"

func TestLoadDefaultsForHTTPRequireAPIKey(t *testing.T) {
	t.Setenv("MCP_API_KEY", "")
	_, err := Load([]string{"--transport", "http"})
	if err == nil {
		t.Fatal("expected error when api key missing for http transport")
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	t.Setenv("MCP_BIND_PORT", "9999")
	t.Setenv("MCP_API_KEY", "from-env")
	cfg, err := Load([]string{"--port", "8081"})
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

func TestExternalEmailDefaultsFromID(t *testing.T) {
	cfg, err := Load([]string{"--api-key", "k", "--external-id", "alice-example-com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alice-example-com@wardrowbe.local"
	if cfg.ExternalEmail != want {
		t.Errorf("default external email: got %q, want %q", cfg.ExternalEmail, want)
	}
}

func TestExternalEmailExplicitWins(t *testing.T) {
	cfg, err := Load([]string{"--api-key", "k", "--external-id", "x", "--external-email", "real@example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExternalEmail != "real@example.com" {
		t.Errorf("explicit external email: got %q", cfg.ExternalEmail)
	}
}

func TestStdioDoesNotRequireAPIKey(t *testing.T) {
	cfg, err := Load([]string{"--transport", "stdio"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("got transport %q", cfg.Transport)
	}
}

func TestInvalidVariantRejected(t *testing.T) {
	_, err := Load([]string{"--api-key", "k", "--image-default-variant", "huge"})
	if err == nil {
		t.Fatal("expected error for invalid image variant")
	}
}

func TestOIDCRequiresFields(t *testing.T) {
	_, err := Load([]string{"--api-key", "k", "--auth", "oidc"})
	if err == nil {
		t.Fatal("expected error: oidc mode missing required fields")
	}
}
