package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
)

func testServer(t *testing.T, portalURL string) *Server {
	t.Helper()
	cfg := config.Config{
		APIKey:            "secret-key",
		ImageVariant:      config.VariantMedium,
		ImageMaxDim:       768,
		PortalResourceURL: portalURL,
		MaxConcurrent:     4,
		MaxBodyBytes:      40 << 20,
	}
	client := wardrowbe.NewClient("http://unused", wardrowbe.DevTokenProvider{ExternalID: "x"}, nil, slog.Default())
	return New(cfg, client, slog.Default())
}

func TestRootProbeIsAnonymous(t *testing.T) {
	srv := testServer(t, "")
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wardrowbe-mcp") {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestMCPWithoutBearerReturns401WithWWWAuthenticate(t *testing.T) {
	portal := "https://portal.example.com/.well-known/oauth-protected-resource"
	srv := testServer(t, portal)
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, `resource_metadata="`+portal+`"`) {
		t.Errorf("WWW-Authenticate = %q, want resource_metadata for portal", got)
	}
}

func TestMCPWithWrongBearerReturns401(t *testing.T) {
	srv := testServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestConcurrencyLimitReturns503WhenSaturated(t *testing.T) {
	srv := testServer(t, "")
	srv.sem = make(chan struct{}, 1) // cap 1 for a deterministic test

	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := srv.limitConcurrency(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// Occupy the only slot.
	go blocking.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/mcp", nil))
	<-entered

	// Second request must be rejected immediately with 503.
	rec := httptest.NewRecorder()
	blocking.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 503")
	}
	close(release)
}

func TestReadyzReturns503WhenBackendUnreachable(t *testing.T) {
	srv := testServer(t, "") // client points at http://unused → Ping fails
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
}

func TestPanicRecoveryReturns500(t *testing.T) {
	srv := testServer(t, "")
	h := srv.recoverPanic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", rec.Code)
	}
}

func TestMCPWithCorrectBearerPassesGate(t *testing.T) {
	srv := testServer(t, "")
	rec := httptest.NewRecorder()
	// A POST with a valid bearer but empty/invalid JSON-RPC body should pass the
	// auth gate and be handled by the MCP transport (i.e. not a 401).
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid bearer should not be rejected by the gate")
	}
	_, _ = io.Copy(io.Discard, rec.Body)
}
