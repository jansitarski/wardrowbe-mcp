// Package mcpserver wires the Wardrowbe backend client into MCP tools and serves
// them over Streamable HTTP (or stdio).
package mcpserver

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverName = "wardrowbe-mcp"

// serverVersion is the build version reported to MCP clients. It is overridden
// at build time via -ldflags
// "-X github.com/jansitarski/wardrowbe-mcp/internal/mcpserver.serverVersion=<v>"
// (see the Dockerfile and Makefile); unbuilt/dev builds report "dev".
var serverVersion = "dev"

// Version returns the build version reported to MCP clients (see serverVersion),
// so the command layer can answer `--version` with the same value.
func Version() string { return serverVersion }

// Server bundles the runtime dependencies shared by every tool handler.
type Server struct {
	cfg    config.Config
	client *wardrowbe.Client
	log    *slog.Logger
	mcp    *server.MCPServer
	// apiKeyHash is the SHA-256 of cfg.APIKey, precomputed once so the
	// per-request bearer comparison doesn't re-hash the configured key.
	apiKeyHash [32]byte
	// imageTransport builds the HTTP transport for external image fetches.
	// Production uses the SSRF-guarded transport; tests inject a plain one to
	// reach a loopback test server. Injecting it here (rather than a package-level
	// var) keeps the seam per-instance and free of data races under -race.
	// It is invoked once: the resulting transport is shared across fetches so
	// idle keep-alive connections are pooled and reaped instead of leaking
	// per call (see imageHTTPTransport).
	imageTransport     func() *http.Transport
	imageTransportOnce sync.Once
	imageTransportInst *http.Transport

	// readyMu guards a short-lived cache of the last backend readiness result so
	// the unauthenticated /readyz endpoint can't be used to drive unbounded
	// backend pings. It is held across the bounded ping itself (see readiness).
	readyMu      sync.Mutex
	readyChecked time.Time
	readyErr     error
}

// New builds the MCP server and registers all tools.
func New(cfg config.Config, client *wardrowbe.Client, log *slog.Logger) *Server {
	mcpSrv := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s := &Server{
		cfg:            cfg,
		client:         client,
		log:            log,
		mcp:            mcpSrv,
		apiKeyHash:     sha256.Sum256([]byte(cfg.APIKey)),
		imageTransport: ssrfTransport,
	}
	s.registerTools()
	return s
}

// MCP exposes the underlying mcp-go server (used to build transports).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// imageHTTPTransport returns the shared transport for external image fetches,
// building it on first use from the injected factory.
func (s *Server) imageHTTPTransport() *http.Transport {
	s.imageTransportOnce.Do(func() {
		s.imageTransportInst = s.imageTransport()
	})
	return s.imageTransportInst
}

// registerTools attaches every tool group. Grouped by domain across files.
func (s *Server) registerTools() {
	s.registerMiscTools()      // health, auth, session, analytics, notifications
	s.registerItemTools()      // list/get items, wear/wash/archive/restore
	s.registerOutfitTools()    // suggest, get, recent, accept/reject/skip, feedback
	s.registerAuthoringTools() // create_outfit_suggestion, create_item_pairing (source=external)
	s.registerImageTools()     // get_item_image, get_outfit_images
	s.registerWritebackTools() // update_item, set_item_tags, set_item_description
	s.registerCreateTools()    // create_item_from_url, create_item_from_base64
}

// add registers a tool with its handler.
func (s *Server) add(tool mcp.Tool, handler server.ToolHandlerFunc) {
	s.mcp.AddTool(tool, handler)
}

// --- shared result helpers ---

// jsonText returns a raw JSON payload as a text result, re-indented for
// readability. If raw is nil (e.g. a 204) it reports success.
func jsonText(raw json.RawMessage) *mcp.CallToolResult {
	if len(raw) == 0 {
		return mcp.NewToolResultText(`{"ok":true}`)
	}
	var pretty json.RawMessage
	if buf, err := indentJSON(raw); err == nil {
		pretty = buf
	} else {
		pretty = raw
	}
	return mcp.NewToolResultText(string(pretty))
}

func indentJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// marshalText marshals an arbitrary value to an indented JSON text result.
func marshalText(v any) (*mcp.CallToolResult, error) {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(buf)), nil
}
