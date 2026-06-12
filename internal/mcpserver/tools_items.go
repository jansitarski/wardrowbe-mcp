package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// defaultListPageSize is applied when list_items is called without page_size,
// so a zero-arg call can't dump the entire wardrobe into the context window.
const defaultListPageSize = 25

// maxArchiveReasonLen mirrors the backend's 50-char limit on the archive
// reason; validating here turns its opaque 422 into a clear tool error.
const maxArchiveReasonLen = 50

func (s *Server) registerItemTools() {
	s.add(mcp.NewTool("wardrowbe_list_items",
		mcp.WithDescription("List wardrobe items with optional filters. Results are paginated: "+
			"page_size defaults to 25 — pass page to fetch more, or a filter/search to narrow. "+
			"The response echoes the effective page/page_size, with the backend payload under \"result\"."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("page", mcp.Description("1-based page number."), mcp.Min(1)),
		mcp.WithInteger("page_size", mcp.Description("Items per page (1-100)."),
			mcp.DefaultNumber(defaultListPageSize), mcp.Min(1), mcp.Max(100)),
		mcp.WithString("category", mcp.Description("Filter by item type/category.")),
		mcp.WithBoolean("is_archived", mcp.Description("Filter by archived state.")),
		mcp.WithBoolean("needs_wash", mcp.Description("Only items needing a wash.")),
		mcp.WithString("search", mcp.Description("Free-text search.")),
	), s.handleListItems)

	s.add(mcp.NewTool("wardrowbe_get_item",
		mcp.WithDescription("Get a single wardrobe item by id."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleGetItem)

	s.add(mcp.NewTool("wardrowbe_get_items_to_wash",
		mcp.WithDescription("List items that currently need washing (shorthand for "+
			"wardrowbe_list_items with needs_wash=true)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("Max items to return (1-20)."),
			mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(20)),
	), s.handleItemsToWash)

	s.add(mcp.NewTool("wardrowbe_log_wear",
		mcp.WithDescription("Log that an item was worn (optionally on a given date)."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("date", mcp.Description("Date worn, YYYY-MM-DD (defaults to today).")),
	), s.handleLogWear)

	s.add(mcp.NewTool("wardrowbe_log_wash",
		mcp.WithDescription("Log that an item was washed."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleLogWash)

	s.add(mcp.NewTool("wardrowbe_archive_item",
		mcp.WithDescription("Archive an item, optionally with a reason. Reversible via "+
			"wardrowbe_restore_item."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("reason", mcp.Description("Why the item is being archived (max 50 chars)."),
			mcp.MaxLength(maxArchiveReasonLen)),
	), s.handleArchiveItem)

	s.add(mcp.NewTool("wardrowbe_restore_item",
		mcp.WithDescription("Restore a previously archived item."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
	), s.handleRestoreItem)
}

func (s *Server) handleListItems(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q := url.Values{}
	page := 1
	if p, present, errRes := argInt(req, "page"); errRes != nil {
		return errRes, nil
	} else if present && p > 0 {
		page = p
		q.Set("page", itoa(page))
	}
	size, present, errRes := argInt(req, "page_size")
	if errRes != nil {
		return errRes, nil
	}
	if !present || size <= 0 {
		size = defaultListPageSize
	}
	size = clampInt(size, 1, 100)
	q.Set("page_size", itoa(size))
	if category := req.GetString("category", ""); category != "" {
		q.Set("category", category)
	}
	if search := req.GetString("search", ""); search != "" {
		q.Set("search", search)
	}
	if v, present, errRes := argBool(req, "is_archived"); errRes != nil {
		return errRes, nil
	} else if present {
		q.Set("is_archived", boolStr(v))
	}
	if v, present, errRes := argBool(req, "needs_wash"); errRes != nil {
		return errRes, nil
	} else if present {
		q.Set("needs_wash", boolStr(v))
	}

	raw, err := s.client.Request(ctx, http.MethodGet, "/items", q, nil)
	if err != nil {
		return toolErr("list items failed", err), nil
	}
	// Echo the effective pagination around the backend payload: the listing is
	// always paginated (page_size defaults to 25), and backend list responses
	// don't reliably carry page metadata — without the echo, a zero-arg call
	// over a larger wardrobe reads as if 25 items were the whole collection.
	envelope, err := json.Marshal(map[string]any{
		"page":      page,
		"page_size": size,
		"result":    json.RawMessage(raw),
	})
	if err != nil {
		return jsonText(raw), nil
	}
	return mcp.NewToolResultText(string(envelope)), nil
}

func (s *Server) handleGetItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/items/"+url.PathEscape(itemID), nil, nil)
	if err != nil {
		return toolErr("get item failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleItemsToWash(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit, errRes := argIntDefault(req, "limit", 20, 1, 20)
	if errRes != nil {
		return errRes, nil
	}
	q := url.Values{"needs_wash": {"true"}, "page_size": {itoa(limit)}}
	raw, err := s.client.Request(ctx, http.MethodGet, "/items", q, nil)
	if err != nil {
		return toolErr("items-to-wash fetch failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleLogWear(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
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
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	return s.itemAction(ctx, itemID, "wash", nil)
}

func (s *Server) handleArchiveItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	var body any
	if reason := req.GetString("reason", ""); reason != "" {
		if utf8.RuneCountInString(reason) > maxArchiveReasonLen {
			return mcp.NewToolResultErrorf("reason too long: %d chars (max %d)",
				utf8.RuneCountInString(reason), maxArchiveReasonLen), nil
		}
		body = map[string]string{"reason": reason}
	}
	return s.itemAction(ctx, itemID, "archive", body)
}

func (s *Server) handleRestoreItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
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
