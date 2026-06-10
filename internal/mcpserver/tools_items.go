package mcpserver

import (
	"context"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerItemTools() {
	s.add(mcp.NewTool("list_items",
		mcp.WithDescription("List wardrobe items with optional filters and pagination."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("page", mcp.Description("1-based page number."), mcp.Min(1)),
		mcp.WithInteger("page_size", mcp.Description("Items per page (1-100)."), mcp.Min(1), mcp.Max(100)),
		mcp.WithString("category", mcp.Description("Filter by item type/category.")),
		mcp.WithBoolean("is_archived", mcp.Description("Filter by archived state.")),
		mcp.WithBoolean("needs_wash", mcp.Description("Only items needing a wash.")),
		mcp.WithString("search", mcp.Description("Free-text search.")),
	), s.handleListItems)

	s.add(mcp.NewTool("get_item",
		mcp.WithDescription("Get a single wardrobe item by id."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleGetItem)

	s.add(mcp.NewTool("get_items_to_wash",
		mcp.WithDescription("List items that currently need washing."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("Max items to return (1-20)."), mcp.Min(1), mcp.Max(20)),
	), s.handleItemsToWash)

	s.add(mcp.NewTool("log_wear",
		mcp.WithDescription("Log that an item was worn (optionally on a given date)."),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("date", mcp.Description("Date worn, YYYY-MM-DD (defaults to today).")),
	), s.handleLogWear)

	s.add(mcp.NewTool("log_wash",
		mcp.WithDescription("Log that an item was washed."),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleLogWash)

	s.add(mcp.NewTool("archive_item",
		mcp.WithDescription("Archive an item, optionally with a reason."),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("reason", mcp.Description("Why the item is being archived.")),
	), s.handleArchiveItem)

	s.add(mcp.NewTool("restore_item",
		mcp.WithDescription("Restore a previously archived item."),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleRestoreItem)
}

func (s *Server) handleListItems(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q := url.Values{}
	if page := req.GetInt("page", 0); page > 0 {
		q.Set("page", itoa(page))
	}
	if size := req.GetInt("page_size", 0); size > 0 {
		q.Set("page_size", itoa(clampInt(size, 1, 100)))
	}
	if category := req.GetString("category", ""); category != "" {
		q.Set("category", category)
	}
	if search := req.GetString("search", ""); search != "" {
		q.Set("search", search)
	}
	if _, ok := req.GetArguments()["is_archived"]; ok {
		q.Set("is_archived", boolStr(req.GetBool("is_archived", false)))
	}
	if _, ok := req.GetArguments()["needs_wash"]; ok {
		q.Set("needs_wash", boolStr(req.GetBool("needs_wash", false)))
	}

	raw, err := s.client.Request(ctx, http.MethodGet, "/items", q, nil)
	if err != nil {
		return toolErr("list items failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleGetItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/items/"+url.PathEscape(itemID), nil, nil)
	if err != nil {
		return toolErr("get item failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleItemsToWash(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := clampInt(req.GetInt("limit", 20), 1, 20)
	q := url.Values{"needs_wash": {"true"}, "page_size": {itoa(limit)}}
	raw, err := s.client.Request(ctx, http.MethodGet, "/items", q, nil)
	if err != nil {
		return toolErr("items-to-wash fetch failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleLogWear(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	var body any
	if date := req.GetString("date", ""); date != "" {
		if !isValidDate(date) {
			return mcp.NewToolResultError("date must be YYYY-MM-DD"), nil
		}
		body = map[string]string{"date": date}
	}
	return s.itemAction(ctx, itemID, "wear", body)
}

func (s *Server) handleLogWash(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	return s.itemAction(ctx, itemID, "wash", nil)
}

func (s *Server) handleArchiveItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	var body any
	if reason := req.GetString("reason", ""); reason != "" {
		body = map[string]string{"reason": reason}
	}
	return s.itemAction(ctx, itemID, "archive", body)
}

func (s *Server) handleRestoreItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	return s.itemAction(ctx, itemID, "restore", nil)
}

// itemAction POSTs /items/{id}/{action} with an optional JSON body.
func (s *Server) itemAction(ctx context.Context, itemID, action string, body any) (*mcp.CallToolResult, error) {
	path := "/items/" + url.PathEscape(itemID) + "/" + action
	raw, err := s.client.Request(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return toolErr(action+" failed", err), nil
	}
	return jsonText(raw), nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
