package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// Pairing read surface: the counterpart to wardrowbe_create_item_pairing, so
// the agent can see what already exists before authoring more.

func (s *Server) registerPairingTools() {
	s.add(mcp.NewTool("wardrowbe_list_pairings",
		mcp.WithDescription("List saved pairings (GET /pairings) — outfits built around a source "+
			"item, both generated and externally authored. Pass item_id to list only that item's "+
			"pairings (GET /pairings/item/{id}). Check here before wardrowbe_create_item_pairing "+
			"to avoid duplicates."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Description("Only pairings built around this source item.")),
		mcp.WithString("source_type",
			mcp.Description("Filter by source item type (e.g. shirt). Ignored when item_id is set.")),
		mcp.WithInteger("page", mcp.Description("1-based page number."), mcp.DefaultNumber(1), mcp.Min(1)),
		mcp.WithInteger("page_size", mcp.Description("Pairings per page (1-100)."),
			mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(100)),
	), s.handleListPairings)
}

func (s *Server) handleListPairings(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	page, errRes := argIntDefault(req, "page", 1, 1, 100000)
	if errRes != nil {
		return errRes, nil
	}
	pageSize, errRes := argIntDefault(req, "page_size", 20, 1, 100)
	if errRes != nil {
		return errRes, nil
	}
	q := url.Values{"page": {itoa(page)}, "page_size": {itoa(pageSize)}}

	path := "/pairings"
	if itemID := strings.TrimSpace(req.GetString("item_id", "")); itemID != "" {
		path = "/pairings/item/" + url.PathEscape(itemID)
	} else if sourceType := req.GetString("source_type", ""); sourceType != "" {
		q.Set("source_type", sourceType)
	}

	raw, err := s.client.Request(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return toolErr("pairings fetch failed", err), nil
	}
	return jsonText(raw), nil
}
