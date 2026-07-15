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

// initializeMCP POSTs a JSON-RPC initialize request through the full HTTP
// handler and returns the recorder, so tests can inspect transport behavior.
func initializeMCP(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-03-26","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"1"}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	return rec
}

func TestStatelessModeIssuesNoSessionID(t *testing.T) {
	srv := testServer(t, "")
	srv.cfg.Stateless = true
	rec := initializeMCP(t, srv)
	if got := rec.Header().Get("Mcp-Session-Id"); got != "" {
		t.Errorf("stateless initialize returned session id %q, want none", got)
	}
}

func TestStatefulModeIssuesSessionID(t *testing.T) {
	srv := testServer(t, "") // testServer leaves cfg.Stateless=false
	rec := initializeMCP(t, srv)
	if rec.Header().Get("Mcp-Session-Id") == "" {
		t.Error("stateful initialize returned no session id")
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
