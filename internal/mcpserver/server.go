// Package mcpserver wires the Wardrowbe backend client into MCP tools and serves
// them over Streamable HTTP (or stdio).
package mcpserver

import (
	"encoding/json"
	"log/slog"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "wardrowbe-mcp"
	serverVersion = "0.2.4"
)

// Server bundles the runtime dependencies shared by every tool handler.
type Server struct {
	cfg    config.Config
	client *wardrowbe.Client
	log    *slog.Logger
	mcp    *server.MCPServer
}

// New builds the MCP server and registers all tools.
func New(cfg config.Config, client *wardrowbe.Client, log *slog.Logger) *Server {
	mcpSrv := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s := &Server{cfg: cfg, client: client, log: log, mcp: mcpSrv}
	s.registerTools()
	return s
}

// MCP exposes the underlying mcp-go server (used to build transports).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// registerTools attaches every tool group. Grouped by domain across files.
func (s *Server) registerTools() {
	s.registerMiscTools()      // health, auth, session, analytics, notifications
	s.registerItemTools()      // list/get items, wear/wash/archive/restore
	s.registerOutfitTools()    // suggest, get, recent, accept/reject/skip, feedback
	s.registerImageTools()     // get_item_image, get_outfit_images
	s.registerWritebackTools() // update_item, set_item_tags, set_item_description
	s.registerCreateTools()    // create_item_from_url
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
